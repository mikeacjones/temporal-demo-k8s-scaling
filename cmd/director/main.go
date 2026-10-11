// Command director runs the dedicated traffic-director worker and makes sure
// the TrafficDirector entity workflow exists. If the (in-memory) dev server
// restarts, the watchdog loop below recreates the entity automatically.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/director"
	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/telemetry"
)

func main() {
	logger, tlog := telemetry.Logger()

	opts, err := envconfig.LoadDefaultClientOptions()
	if err != nil {
		logger.Error("load client options", "error", err)
		os.Exit(1)
	}
	if opts.HostPort == "" {
		opts.HostPort = "localhost:7233"
	}
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	opts.Logger = tlog
	if opts.MetricsHandler, err = telemetry.MetricsHandler(telemetry.Labels{Namespace: opts.Namespace}); err != nil {
		logger.Error("metrics", "error", err)
		os.Exit(1)
	}

	srv := telemetry.NewServer(telemetry.Env("HTTP_ADDR", ":9090"))
	srv.Start(logger)

	c, err := dialWithRetry(opts, logger)
	if err != nil {
		logger.Error("dial temporal", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	acts := &director.Activities{
		Client:               c,
		K8s:                  k8sClient(logger),
		K8sNamespace:         telemetry.Env("WORKER_K8S_NAMESPACE", "demo"),
		WorkerDeploymentName: telemetry.Env("WORKER_DEPLOYMENT", "order-worker"),
	}

	w := worker.New(c, director.TaskQueue, worker.Options{
		WorkflowTaskPollerBehavior: worker.NewPollerBehaviorAutoscaling(worker.PollerBehaviorAutoscalingOptions{}),
		ActivityTaskPollerBehavior: worker.NewPollerBehaviorAutoscaling(worker.PollerBehaviorAutoscalingOptions{}),
	})
	w.RegisterWorkflowWithOptions(director.TrafficDirectorWorkflow, workflow.RegisterOptions{Name: director.WorkflowName})
	w.RegisterActivity(acts)
	if err := w.Start(); err != nil {
		logger.Error("start worker", "error", err)
		os.Exit(1)
	}
	srv.MarkReady()
	logger.Info("traffic director worker started")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Keep the entity alive: start it now and re-check periodically, so a dev
	// server restart (which wipes in-memory state) heals itself.
	ensure := func() {
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                       director.WorkflowID,
			TaskQueue:                director.TaskQueue,
			WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		}, director.WorkflowName, director.State{Config: director.DefaultConfig()})
		if err != nil {
			logger.Warn("ensure traffic director", "error", err)
			return
		}
		logger.Debug("traffic director running", "runId", run.GetRunID())
	}
	ensure()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			w.Stop()
			srv.Shutdown()
			return
		case <-t.C:
			ensure()
		}
	}
}

func dialWithRetry(opts client.Options, logger *slog.Logger) (client.Client, error) {
	var lastErr error
	for range 60 {
		c, err := client.Dial(opts)
		if err == nil {
			return c, nil
		}
		lastErr = err
		logger.Warn("waiting for temporal", "hostPort", opts.HostPort, "error", err)
		time.Sleep(2 * time.Second)
	}
	return nil, lastErr
}

func k8sClient(logger *slog.Logger) dynamic.Interface {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: os.Getenv("KUBE_CONTEXT")},
		).ClientConfig()
		if err != nil {
			logger.Warn("no kubernetes config; automatic rollouts disabled", "error", err)
			return nil
		}
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logger.Warn("kubernetes client", "error", err)
		return nil
	}
	return dc
}
