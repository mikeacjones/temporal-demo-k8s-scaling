package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// Terminal streams a real k9s (launched in a PTY) to the browser, so the
// booth can prove the pod scaling is genuine Kubernetes activity.
//
// Protocol: binary frames carry raw terminal bytes in both directions; text
// frames from the browser carry JSON control messages ({"type":"resize"}).
type Terminal struct {
	Command []string // e.g. ["scripts/k9s.sh"]
	Env     []string // extra environment, e.g. PROFILE=temporal-demo
	Logger  *slog.Logger

	active atomic.Int32
}

// maxSessions bounds how many k9s processes viewers can spawn at once.
const maxSessions = 4

type termControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (t *Terminal) Available() bool {
	if len(t.Command) == 0 {
		return false
	}
	_, err := exec.LookPath(t.Command[0])
	return err == nil
}

func (t *Terminal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !t.Available() {
		http.Error(w, "k9s launcher not found", http.StatusServiceUnavailable)
		return
	}
	if t.active.Add(1) > maxSessions {
		t.active.Add(-1)
		http.Error(w, "too many terminal sessions", http.StatusTooManyRequests)
		return
	}
	defer t.active.Add(-1)

	// Accept rejects cross-origin requests by default, so a random web page
	// in the booth browser can't attach to this terminal.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.Command(t.Command[0], t.Command[1:]...)
	cmd.Env = append(os.Environ(), append([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}, t.Env...)...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 24})
	if err != nil {
		t.Logger.Warn("start k9s", "error", err)
		_ = conn.Close(websocket.StatusInternalError, "failed to start k9s")
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
	}()

	// PTY -> browser.
	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "k9s exited")
				return
			}
		}
	}()

	// Browser -> PTY.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var c termControl
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" && c.Cols > 0 && c.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: c.Cols, Rows: c.Rows})
			}
			continue
		}
		if _, err := ptmx.Write(data); err != nil {
			return
		}
	}
}
