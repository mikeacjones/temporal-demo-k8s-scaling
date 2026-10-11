// Package telemetry wires up logging, SDK metrics, and the small HTTP server
// (metrics + health) that every worker process in this demo exposes.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/log"
)

// Labels are attached to every SDK metric. The names match the tokens the
// Temporal Worker Controller substitutes into WorkerResourceTemplates, so a
// per-version KEDA Prometheus trigger can select exactly one Build ID.
type Labels struct {
	DeploymentName string // Temporal deployment name, e.g. "demo/order-worker"
	BuildID        string
	Namespace      string
	Pod            string
}

func (l Labels) attributes() attribute.Set {
	kv := []attribute.KeyValue{
		// The controller's tokens use "_" instead of "/" so the value is a valid k8s label.
		attribute.String("temporal_worker_deployment_name", strings.Replace(l.DeploymentName, "/", "_", 1)),
		attribute.String("temporal_worker_build_id", l.BuildID),
		attribute.String("temporal_namespace", l.Namespace),
	}
	if l.Pod != "" {
		kv = append(kv, attribute.String("pod", l.Pod))
	}
	return attribute.NewSet(kv...)
}

// Logger returns a logfmt slog logger (readable in k9s's log view) and a
// Temporal SDK adapter for it.
func Logger() (*slog.Logger, log.Logger) {
	s := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return s, sdkLogger{s}
}

// sdkLogger adapts the SDK's logs for a booth audience. The demo injects
// activity failures on purpose (transient errors, out of stock); the SDK
// reports each as ERROR "Activity error.", which reads as breakage. Retries
// and compensation are what Temporal is for, so they're logged as a WARN that
// says what happens next.
type sdkLogger struct{ l *slog.Logger }

func (s sdkLogger) Debug(msg string, kv ...any) {}
func (s sdkLogger) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s sdkLogger) Warn(msg string, kv ...any)  { s.l.Warn(msg, kv...) }
func (s sdkLogger) Error(msg string, kv ...any) {
	if msg == "Activity error." {
		fields := pick(kv, "ActivityType", "Attempt", "WorkflowID", "Error")
		if nonRetryable(kv) {
			// e.g. out of stock: the workflow compensates (refund + release).
			s.l.Warn("activity failed permanently; the workflow compensates", fields...)
		} else {
			s.l.Warn("activity attempt failed; Temporal will retry it", fields...)
		}
		return
	}
	s.l.Error(msg, kv...)
}

func nonRetryable(kv []any) bool {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, _ := kv[i].(string); k == "Error" {
			return strings.Contains(fmt.Sprint(kv[i+1]), "retryable: false")
		}
	}
	return false
}

func pick(kv []any, keys ...string) []any {
	var out []any
	for _, want := range keys {
		for i := 0; i+1 < len(kv); i += 2 {
			if k, _ := kv[i].(string); k == want {
				out = append(out, kv[i], kv[i+1])
			}
		}
	}
	return out
}

// MetricsHandler builds an OpenTelemetry-backed Temporal metrics handler that
// is exported in Prometheus format by Server.
func MetricsHandler(labels Labels) (client.MetricsHandler, error) {
	exporter, err := otelprom.New()
	if err != nil {
		return nil, err
	}
	return opentelemetry.NewMetricsHandler(opentelemetry.MetricsHandlerOptions{
		Meter:             sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)).Meter("temporal-k8s-scaling-demo"),
		InitialAttributes: labels.attributes(),
	}), nil
}

// Server serves /metrics and /healthz. Readiness flips to OK once MarkReady is called.
type Server struct {
	srv   *http.Server
	ready atomic.Bool
}

func NewServer(addr string) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	s.srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) Start(logger *slog.Logger) {
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("telemetry server failed", "error", err)
		}
	}()
}

func (s *Server) MarkReady() { s.ready.Store(true) }

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// Env returns the environment variable or a default.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ClientOptions loads Temporal connection settings from TEMPORAL_* environment
// variables only (the ones the Worker Controller injects). The CLI config file
// is deliberately ignored so a developer's default profile (e.g. pointing at
// Temporal Cloud) can never redirect the demo away from the local dev server.
func ClientOptions() (client.Options, error) {
	opts, err := envconfig.LoadClientOptions(envconfig.LoadClientOptionsRequest{DisableFile: true})
	if err != nil {
		return opts, err
	}
	if opts.HostPort == "" {
		opts.HostPort = "localhost:7233"
	}
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	return opts, nil
}
