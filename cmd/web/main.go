// Command web serves the booth dashboard. It runs on the host next to the
// Temporal dev server and reads cluster state through the local kubeconfig.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/dashboard"
	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/telemetry"
	"github.com/mikeacjones/temporal-k8s-scaling-demo/web"
)

func main() {
	addr := flag.String("addr", telemetry.Env("WEB_ADDR", "127.0.0.1:8080"), "listen address")
	promURL := flag.String("prometheus", telemetry.Env("PROMETHEUS_URL", "http://localhost:9091"), "Prometheus base URL")
	kubeContext := flag.String("kube-context", telemetry.Env("KUBE_CONTEXT", "temporal-demo"), "kubeconfig context")
	k8sNS := flag.String("k8s-namespace", telemetry.Env("K8S_NAMESPACE", "demo"), "namespace of the demo workloads")
	repoURL := flag.String("repo", telemetry.Env("REPO_URL", "https://github.com/mikeacjones/temporal-k8s-scaling-demo"), "URL for the QR code")
	uiURL := flag.String("temporal-ui", telemetry.Env("TEMPORAL_UI_URL", "http://localhost:8233"), "Temporal Web UI URL for the header link")
	flag.Parse()

	logger, tlog := telemetry.Logger()

	opts, err := telemetry.ClientOptions()
	if err != nil {
		logger.Error("load client options", "error", err)
		os.Exit(1)
	}
	opts.Logger = tlog
	// Lazy dial: the dashboard should come up (and show "reconnecting") even
	// before the dev server is listening.
	tc, err := client.NewLazyClient(opts)
	if err != nil {
		logger.Error("temporal client", "error", err)
		os.Exit(1)
	}
	defer tc.Close()

	k8s, err := kubeClient(*kubeContext)
	if err != nil {
		logger.Warn("kubernetes unavailable; pod view disabled", "error", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	col := dashboard.NewCollector(dashboard.Config{
		Temporal:             tc,
		Namespace:            opts.Namespace,
		K8s:                  k8s,
		K8sNamespace:         *k8sNS,
		WorkerDeploymentName: "order-worker",
		DirectorDeployment:   "traffic-director",
		PrometheusURL:        *promURL,
		Logger:               logger,
	})
	col.Run(ctx)

	static, _ := fs.Sub(web.Static, "static")
	srv := dashboard.NewServer(col, tc, static, *repoURL, *uiURL)
	go srv.Run(ctx)

	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = hs.Shutdown(sctx)
	}()
	logger.Info("dashboard listening", "addr", *addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server", "error", err)
		os.Exit(1)
	}
}

func kubeClient(context string) (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: context},
		).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	cfg.Timeout = 3 * time.Second
	return kubernetes.NewForConfig(cfg)
}
