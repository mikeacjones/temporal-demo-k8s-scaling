// Command worker runs the order-processing worker. It is deployed by the
// Temporal Worker Controller as a versioned WorkerDeployment and autoscaled
// by KEDA. Its slot counts are deliberately small so traffic forces scaling.
package main

import (
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/orders"
	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/telemetry"
)

func main() {
	logger, tlog := telemetry.Logger()

	// Injected by the Worker Controller into every versioned pod.
	deploymentName := os.Getenv("TEMPORAL_DEPLOYMENT_NAME")
	buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
	versioned := deploymentName != "" && buildID != ""

	opts, err := telemetry.ClientOptions()
	if err != nil {
		logger.Error("load client options", "error", err)
		os.Exit(1)
	}
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	opts.Logger = tlog
	opts.MetricsHandler, err = telemetry.MetricsHandler(telemetry.Labels{
		DeploymentName: deploymentName,
		BuildID:        buildID,
		Namespace:      opts.Namespace,
		Pod:            os.Getenv("POD_NAME"),
	})
	if err != nil {
		logger.Error("metrics", "error", err)
		os.Exit(1)
	}

	srv := telemetry.NewServer(telemetry.Env("HTTP_ADDR", ":9090"))
	srv.Start(logger)

	c, err := client.Dial(opts)
	if err != nil {
		logger.Error("dial temporal", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	label := telemetry.Env("VERSION_LABEL", "aurora")
	wopts := worker.Options{
		// Small, fixed slot counts: each pod can only run a handful of
		// activities at once, so a traffic ramp quickly saturates it and
		// KEDA has to add pods. Fixed-size slots also keep
		// temporal_worker_task_slots_available meaningful for slot utilization.
		MaxConcurrentActivityExecutionSize:     envInt("ACTIVITY_SLOTS", 4),
		MaxConcurrentWorkflowTaskExecutionSize: envInt("WORKFLOW_SLOTS", 10),
		// Poller autoscaling lets the SDK size pollers to demand.
		WorkflowTaskPollerBehavior: worker.NewPollerBehaviorAutoscaling(worker.PollerBehaviorAutoscalingOptions{}),
		ActivityTaskPollerBehavior: worker.NewPollerBehaviorAutoscaling(worker.PollerBehaviorAutoscalingOptions{}),
	}
	if versioned {
		wopts.DeploymentOptions = worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: deploymentName,
				BuildID:        buildID,
			},
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
		}
	}

	w := worker.New(c, orders.TaskQueue, wopts)
	w.RegisterWorkflowWithOptions(orders.OrderWorkflow, workflow.RegisterOptions{
		Name:               orders.WorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	w.RegisterActivity(&orders.Activities{
		VersionLabel: label,
		Speed:        envFloat("SPEED", 1),
		FailureRate:  envFloat("FAILURE_RATE", 0.03),
		Log:          logger,
	})

	if err := w.Start(); err != nil {
		logger.Error("start worker", "error", err)
		os.Exit(1)
	}
	srv.MarkReady()
	logger.Info("order worker started", "deployment", deploymentName, "buildId", buildID, "label", label,
		"activitySlots", wopts.MaxConcurrentActivityExecutionSize)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("shutting down")
	w.Stop()
	srv.Shutdown()
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return def
}
