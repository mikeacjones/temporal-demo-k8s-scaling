package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"go.temporal.io/sdk/client"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/director"
)

const historyLen = 600 // 10 minutes at 1 sample/second

// Server streams snapshots over SSE and exposes staff controls.
type Server struct {
	collector *Collector
	temporal  client.Client
	static    fs.FS
	repoURL   string
	uiURL     string

	// Terminal, when set, serves an embedded k9s at /api/k9s.
	Terminal *Terminal
	// TerminalInteractive reports that k9s can delete pods (the UI shows hints).
	TerminalInteractive bool

	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	history []Point
	latest  []byte
}

func NewServer(c *Collector, tc client.Client, static fs.FS, repoURL, temporalUIURL string) *Server {
	return &Server{collector: c, temporal: tc, static: static, repoURL: repoURL, uiURL: temporalUIURL, subs: map[chan []byte]struct{}{}}
}

// Run samples the collector once per second and fans the snapshot out.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		snap := s.collector.Snapshot()
		data, err := json.Marshal(snap)
		if err != nil {
			continue
		}
		msg := []byte(fmt.Sprintf("event: snapshot\ndata: %s\n\n", data))
		s.mu.Lock()
		s.history = append(s.history, snap.Point)
		if len(s.history) > historyLen {
			s.history = s.history[len(s.history)-historyLen:]
		}
		s.latest = data
		for ch := range s.subs {
			select {
			case ch <- msg:
			default: // slow client; drop this frame
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(s.static))
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		data := s.latest
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /qr.png", s.qr)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"temporalUi": s.uiURL,
			"repo":       s.repoURL,
			"k9s":        s.Terminal != nil && s.Terminal.Available(),
			"k9sWrite":   s.TerminalInteractive,
		})
	})
	if s.Terminal != nil {
		mux.Handle("GET /api/k9s", s.Terminal)
	}
	mux.HandleFunc("POST /api/pause", s.signal(director.SignalPause))
	mux.HandleFunc("POST /api/resume", s.signal(director.SignalResume))
	mux.HandleFunc("POST /api/rollout", s.signal(director.SignalRolloutNow))
	mux.HandleFunc("POST /api/surge", s.surge)
	mux.HandleFunc("POST /api/config", s.config)
	return mux
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 8)
	s.mu.Lock()
	hist, _ := json.Marshal(s.history)
	latest := s.latest
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	fmt.Fprintf(w, "retry: 2000\nevent: history\ndata: %s\n\n", hist)
	if latest != nil {
		fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", latest)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	png, err := qrcode.Encode(s.repoURL, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=3600")
	_, _ = w.Write(png)
}

func (s *Server) signal(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := s.temporal.SignalWorkflow(r.Context(), director.WorkflowID, "", name, nil)
		writeResult(w, err)
	}
}

func (s *Server) surge(w http.ResponseWriter, r *http.Request) {
	var req director.Surge
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body = defaults chosen by the workflow
	err := s.temporal.SignalWorkflow(r.Context(), director.WorkflowID, "", director.SignalSurge, req)
	writeResult(w, err)
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	var cfg director.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h, err := s.temporal.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
		WorkflowID:   director.WorkflowID,
		UpdateName:   director.UpdateSetConfig,
		Args:         []any{cfg},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err == nil {
		err = h.Get(r.Context(), &cfg)
	}
	writeResult(w, err)
}

func writeResult(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
