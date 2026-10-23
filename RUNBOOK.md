# Runbook: Temporal Worker Autoscaling on Kubernetes

This runbook is for presenting the demo. The [README](README.md) explains what it is and how it's built.

There is no hosted copy on the demo catalog yet, so every step here runs on your own machine. When the catalog copy exists, the setup, walkthrough, and reset sections will describe it, and the local steps will move to [Run it locally](#run-it-locally).

## What it shows

An online store's order traffic rises and falls in waves, and the team ships a new Worker version in the middle of it. Orders keep flowing the whole time.

The demo makes three problems visible:

- **A fixed pool of Workers is either too small at peak or wasted when it's quiet.** Each pod runs only 4 Activities at once, so when traffic ramps up, orders wait in the Task Queue backlog. KEDA adds pods until the backlog clears, then removes them as traffic falls.
- **Deploying during the rush can send in-flight orders to new code halfway through.** A rollout starts a new Worker Deployment Version that takes 25%, then 50%, then all new orders. Orders that started on the old version finish there, and the old version's pods go away once it has drained.
- **A pod dies in the middle of an order.** You delete a Worker pod from the k9s panel. Kubernetes starts a replacement, and Temporal retries the interrupted Activity on another pod of the same version. The order completes.

## Before you start

- Give Docker Desktop at least 8 GB of memory and 6 CPUs. Turn off sleep and the screen saver.
- Install `minikube`, `kubectl`, `helm`, Go 1.27 or later, and the [Temporal CLI](https://docs.temporal.io/cli).
- Run `make up` once before the event. The first run downloads charts and images and takes a few minutes. Later runs are quick.
- If something else already uses port 7233, such as another dev server, see [Use a different Temporal port](#use-a-different-temporal-port).
- Everything runs locally, against a Temporal dev server. Nothing runs on Temporal Cloud, and the demo uses no Actions.

## Start it

1. Run `make up` from the repository root.
2. Open <http://localhost:8080/?kiosk=1&k9s=1> on the screen the audience sees.
3. Open the Temporal UI at <http://localhost:8233> in another tab, for proof.

A healthy start shows:

- **Traffic cycle** moving through idle, with about 0.6 orders per second.
- One version, `aurora`, marked `current` under **Worker versions**, with one pod.
- The k9s panel listing the `demo` namespace: one `order-worker` pod, the `traffic-director` pod, and the `dashboard` pod.
- **Staff controls** at the bottom right, with **Surge ⚡**, **Pause**, and **Roll out version**.

Press `k` to show or hide the k9s panel, and `s` to collapse the staff controls. Add `?controls=0` to the URL to hide them entirely, for example on a second screen.

## The walkthrough

The traffic director runs the same cycle forever: 30 seconds idle, 90 seconds ramping up, 60 seconds at peak, 90 seconds ramping down, and 30 seconds of cooldown, about 5 minutes in all. Every third cycle it rolls out a new version on its own. You can present the beats in the order the cycle reaches them, or start a rollout or a surge whenever you want.

### 1. Traffic is quiet and one pod is enough

1. Wait for the **Traffic cycle** bar to reach idle or cooldown.
2. The audience sees about 0.6 orders per second, a backlog near 0, and one pod per version in **Pod constellation**. Each pip in a pod is one Activity slot.
3. Say: "Every order is a Temporal Workflow: validate, reserve stock, charge, ship, notify. Each pod can run only four Activities at a time, on purpose. Right now one pod keeps up."

### 2. Traffic ramps up and KEDA adds pods

1. Let the cycle reach ramp-up, or press **Surge ⚡**. A surge sends 1.5 times the peak rate, 15 orders per second, for 60 seconds.
2. **Task backlog** climbs first. Within a few seconds, **Worker pods** shows more desired than ready, new pods appear in the constellation and in k9s, and the backlog line in **Traffic vs. capacity** falls back.
3. Say: "Orders are arriving faster than four slots per pod can handle, so they wait in the Task Queue. KEDA reads that backlog from Temporal and adds pods. As soon as a pod is ready, it starts polling and the backlog drains."
4. For proof, run `make status` in a terminal. It lists the ScaledObject and the HPA KEDA created for the version, with the current and desired replica counts.

### 3. Traffic falls and the pods go away

1. Let the cycle reach ramp-down.
2. **Slot utilization** falls, and pods are removed two at a time until one is left. They leave the constellation and disappear from k9s.
3. Say: "Nobody scaled this down. Kubernetes waits 30 seconds to make sure the drop is real, then removes pods. If a removed pod was still running an Activity, Temporal retries it on another pod."

### 4. Kill a pod in the middle of an order

This works best during peak, when every pod is busy.

1. Click the k9s panel. Its border turns green, and your keystrokes now go to k9s.
2. Use `↑` and `↓` to pick an `order-worker` pod. Press `ctrl-d`, Tab to **OK**, and press Enter.
3. Within about 2 seconds the dashboard shows the pod terminating and its replacement starting.
4. Press `l` on a pod to show its live logs: orders being paid for and shipped, and Temporal retrying the failures the demo injects. Press `Esc` to go back.
5. Click anywhere outside the panel to give the keyboard back to the dashboard.
6. Say: "I just deleted a Worker that was in the middle of orders. Kubernetes replaced the pod, and Temporal retries the Activities that pod was running on another one. No order is lost and none is charged twice, and there's no recovery code in the app."

The k9s panel can delete pods in the `demo` namespace and read their logs. It can't scale or delete Deployments, exec into pods, or see other namespaces.

### 5. Roll out a new version during traffic

1. Press **Roll out version**, or wait for the automatic rollout. The **Traffic cycle** header shows how many cycles remain until the next one.
2. A new version, such as `nebula`, appears under **Worker versions** as `ramping 25%`, with its own pod. After 45 seconds it moves to 50%, and after another 45 seconds it becomes `current`.
3. The old version shows `draining` while its orders finish, then `drained`. About 30 seconds later its pods scale to zero. Its Deployment is deleted 2 minutes after that.
4. Say: "We shipped new Worker code in the middle of traffic. Temporal sent a quarter of new orders to it, then half, then all of them. Every order that started on the old version finished on the old version. Each version scales on its own backlog, so the new one grows as it takes over and the old one shrinks."
5. For proof, use any of these:
   - In the Temporal UI, open an order Workflow that started before the rollout. Its result's `versionLabel` names the version it started on.
   - Run `make status`. It shows one ScaledObject and one HPA per version.
   - Run `temporal --disable-config-file --address localhost:7233 worker deployment describe --name demo/order-worker`. It lists each version with its status and ramp percentage.

### 6. Show the traffic director (optional)

Use this beat when the audience asks where the traffic comes from.

1. In the Temporal UI, open the Workflow `traffic-director`.
2. Say: "The traffic itself is a Temporal Workflow. It starts a batch of orders every 2 seconds, sized by the traffic curve, and the staff buttons are Signals to it. Every cycle it continues as new, so its history stays short however long the demo runs."

## Show it in code

All links point at commit [`838fe9c`](https://github.com/mikeacjones/temporal-demo-k8s-scaling/commit/838fe9ccddad700efb67fff42881f22d08b8ce2d). Open a file locally with `code -g <path>:<line>`.

### Pin each order to the version it started on

**When to use it:** after beat 5, or when someone asks how old orders avoid the new code.

**Where it is:** [`cmd/worker/main.go` lines 70–85](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/cmd/worker/main.go#L70-L85) (`code -g cmd/worker/main.go:70`)

```go
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
```

**What you need to understand:**

- `deploymentName` and `buildID` come from the `TEMPORAL_DEPLOYMENT_NAME` and `TEMPORAL_WORKER_BUILD_ID` environment variables, which the Worker Controller sets on every pod it creates. The code never chooses a version.
- `Pinned` means an order runs every step on the version it started on, even after a newer version becomes current. Its Activities go to pods of that version too.
- New orders go to whichever version Temporal's routing says: the current one, or the ramping one for its share during a rollout.
- Pinned suits short Workflows like these orders, which finish in seconds. A Workflow that runs for weeks would keep its old version alive that long. For those, `AutoUpgrade` moves them to the new version, and the code change has to be compatible with their history.
- There is no version check or patch in the Workflow code. Rolling out a new version is the whole change.

**Docs:** [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning)

### Give each pod only four Activity slots

**When to use it:** at beat 1 or 2, or when someone asks why the demo needs so many pods for so little traffic.

**Where it is:** [`cmd/worker/main.go` lines 59–69](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/cmd/worker/main.go#L59-L69) (`code -g cmd/worker/main.go:59`), set from [`deploy/k8s/order-worker.yaml` lines 54–59](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/deploy/k8s/order-worker.yaml#L54-L59)

```go
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
```

**What you need to understand:**

- A slot is one Activity a Worker can run at a time. When all four are busy, the pod stops taking Activity Tasks, and new ones wait in the Task Queue. That wait is the backlog KEDA scales on.
- Four is deliberately small, so a few orders per second fill a pod. A real Worker usually runs far more, and the right number depends on what its Activities do.
- Poller autoscaling lets the SDK add and remove pollers as demand changes, so you don't tune poller counts by hand.
- The Activities sleep to simulate work. A pod uses almost no CPU, which is why scaling on CPU wouldn't work here.

**Docs:** [Worker performance](https://docs.temporal.io/develop/worker-performance)

### Declare the rollout once

**When to use it:** at beat 5, or when someone asks what the team has to do to ship a version.

**Where it is:** [`deploy/k8s/order-worker.yaml` lines 4–39](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/deploy/k8s/order-worker.yaml#L4-L39) (`code -g deploy/k8s/order-worker.yaml:4`)

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: order-worker
spec:
  workerOptions:
    connectionRef:
      name: dev-server
    temporalNamespace: default
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 25
        pauseDuration: 45s
      - rampPercentage: 50
        pauseDuration: 45s
  sunset:
    # Once a version has drained its pinned workflows: scale it to zero
    # shortly after, and delete its Deployment a bit later.
    scaledownDelay: 30s
    deleteDelay: 2m
  deployment:
    # Replicas are owned by the per-version KEDA ScaledObject.
    template:
      # ...
        containers:
          - name: worker
            image: order-worker:dev
```

**What you need to understand:**

- A `WorkerDeployment` is a custom resource from the [Temporal Worker Controller](https://github.com/temporalio/temporal-worker-controller). Any change to the pod template, such as a new image tag, creates a new Build ID. The controller then creates a Deployment for it and registers the version with Temporal.
- `Progressive` sends 25% of new orders to the new version for 45 seconds, then 50% for 45 seconds, then makes it current. The steps are short so the audience sees a whole rollout. A real service would use longer steps.
- `sunset` controls cleanup. Once Temporal reports the old version drained, the controller scales it to zero after 30 seconds and deletes its Deployment 2 minutes later.
- The template sets no `replicas`. The per-version ScaledObject owns the replica count.

**Docs:** [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning), [Temporal Worker Controller](https://github.com/temporalio/temporal-worker-controller)

### Scale each version on its own backlog and slot use

**When to use it:** after beat 2 or beat 5, or when someone asks what KEDA watches.

**Where it is:** [`deploy/k8s/order-worker-autoscaling.yaml` lines 6–67](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/deploy/k8s/order-worker-autoscaling.yaml#L6-L67) (`code -g deploy/k8s/order-worker-autoscaling.yaml:6`)

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: order-worker-keda
spec:
  workerDeploymentRef:
    name: order-worker
  template:
    apiVersion: keda.sh/v1alpha1
    kind: ScaledObject
    spec:
      scaleTargetRef: {}
      # >= 1 so a new version is always polling (rollouts need pollers).
      minReplicaCount: 1
      # ...
      triggers:
        - type: temporal
          useCachedMetrics: true
          metadata:
            endpoint: host.minikube.internal:7233
            taskQueue: orders
            queueTypes: workflow,activity
            targetQueueSize: "5"
            # Empty = filled in per version by the Worker Controller.
            namespace: ""
            workerDeploymentName: ""
            workerDeploymentBuildId: ""
        - type: prometheus
          useCachedMetrics: true
          metricType: Value
          metadata:
            serverAddress: http://prometheus-server.monitoring.svc:80
            threshold: "0.7"
            query: >-
              max(temporal_slot_utilization{
                temporal_worker_deployment_name="{{temporal_worker_deployment_name}}",
                temporal_worker_build_id="{{temporal_worker_build_id}}",
                temporal_namespace="{{temporal_namespace}}",
                worker_type="ActivityWorker"
              }) or vector(0)
```

**What you need to understand:**

- The `WorkerResourceTemplate` is a template, not a ScaledObject. For each version, the Worker Controller renders one ScaledObject, points `scaleTargetRef` at that version's Deployment, and fills in the empty fields and the `{{...}}` tokens with that version's values. It deletes the ScaledObject when the version is removed.
- The `temporal` trigger asks Temporal for this version's backlog on the `orders` Task Queue and wants one pod for every 5 waiting tasks. It reacts quickly when traffic arrives.
- The `prometheus` trigger reads the busiest pod's slot use for this version from the Workers' SDK metrics, and adds pods above 70%. It keeps busy pods running when the backlog is briefly empty, because backlog doesn't count Activities that are already running.
- KEDA runs whichever trigger asks for more pods. `minReplicaCount: 1` keeps every version polling, which a rollout needs to send it traffic.
- The `{{temporal_worker_build_id}}` tokens in the Prometheus query need a Worker Controller build that includes [#616](https://github.com/temporalio/temporal-worker-controller/pull/616). The v1.12.0 release doesn't, so the demo pins a build from `main`. Without it, every version would scale on the whole deployment's slot use, and draining versions would stay scaled up.
- The ScaledObject sets no `maxReplicaCount`, so KEDA's default of 100 applies. In practice the minikube cluster's CPU and memory set the limit.
- Lines 22–36 set how fast pods come and go: up to 4 pods every 10 seconds on the way up, and on the way down 2 pods every 15 seconds, using the highest pod count recommended in the last 30 seconds.

**Docs:** [KEDA Temporal scaler](https://keda.sh/docs/latest/scalers/temporal/), [Worker Controller scaling recommendations](https://github.com/temporalio/temporal-worker-controller/blob/main/docs/scaling-recommendations.md)

### Start a rollout by changing the pod template

**When to use it:** after beat 5, when someone asks what **Roll out version** does.

**Where it is:** [`internal/director/activities.go` lines 90–128](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/internal/director/activities.go#L90-L128) (`code -g internal/director/activities.go:90`)

```go
// RolloutVersion bumps the version annotation on the order worker's pod
// template to the next codename. The Worker Controller does the rest.
func (a *Activities) RolloutVersion(ctx context.Context) (RolloutInfo, error) {
	// ...
	wd, err := res.Get(ctx, a.WorkerDeploymentName, metav1.GetOptions{})
	// ...
	to := VersionLabels[(slices.Index(VersionLabels, from)+1)%len(VersionLabels)]
	// ...
	cur["metadata"] = map[string]any{"annotations": map[string]string{VersionAnnotation: to}}
	body, _ := json.Marshal(patch)
	if _, err := res.Patch(ctx, a.WorkerDeploymentName, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "traffic-director"}); err != nil {
		return RolloutInfo{From: from, To: to}, fmt.Errorf("patch WorkerDeployment: %w", err)
	}
	// ...
}
```

**What you need to understand:**

- This is demo machinery. In a real team, a new image tag from CI changes the pod template. Here an Activity changes one annotation, `demo.temporal.io/version-label`, so the demo can roll out without a build.
- Any change to the pod template gives the Worker Controller a new Build ID, so this one patch starts the whole rollout in the previous two entries.
- The Worker reads the annotation back through the downward API as `VERSION_LABEL`. That's where the codenames on screen and the `versionLabel` in each order's result come from.
- The director's ServiceAccount can only `get` and `patch` WorkerDeployments in the `demo` namespace ([`deploy/k8s/traffic-director.yaml` lines 8–16](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/deploy/k8s/traffic-director.yaml#L8-L16)).
- A customer would leave this out. Their rollout starts when they deploy.

### Run the traffic as an entity Workflow

**When to use it:** at beat 6, or when someone asks how the demo runs for days without a person.

**Where it is:** [`internal/director/workflow.go` lines 192–256](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/internal/director/workflow.go#L192-L256) (`code -g internal/director/workflow.go:192`)

```go
for {
	drain()

	if state.Elapsed == 0 && !state.Paused && state.rolloutDueThisCycle() {
		state.RolloutRequested = true
	}
	if state.RolloutRequested {
		// ...
		err := workflow.ExecuteActivity(actCtx, a.RolloutVersion).Get(ctx, &res)
		// ...
	}

	// ...
	if count > 0 {
		err := workflow.ExecuteLocalActivity(laCtx, a.StartBatch, BatchRequest{
			Prefix: fmt.Sprintf("order-%s-%d", state.IDPrefix, state.Cycle),
			First:  state.Seq,
			Count:  count,
		}).Get(ctx, &started)
		// ...
	}

	if err := workflow.Sleep(ctx, time.Duration(state.Config.TickSeconds)*time.Second); err != nil {
		return err
	}
	// ...
	if cycleDone || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
		if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
			return err
		}
		drain()
		return workflow.NewContinueAsNewError(ctx, WorkflowName, state)
	}
}
```

**What you need to understand:**

- There is one director Workflow, with the ID `traffic-director`, on its own Worker and Task Queue, so scaling the order Workers never affects it.
- Each tick it starts a batch of orders with a Local Activity and then sleeps 2 seconds on a durable timer. A batch that fails isn't retried; the next tick starts a fresh one.
- **Surge ⚡**, **Pause**, and **Roll out version** send Signals. `drain()` applies them at the start of each tick. The `set-config` Update changes the traffic curve while it runs, and its validator rejects a bad config before it reaches the Workflow.
- At the end of each cycle the Workflow continues as new and carries its state forward, so its history never grows past one cycle. It waits for running handlers to finish first, so no Signal or Update is lost.
- The director's Worker re-starts the Workflow every 15 seconds if it isn't running ([`cmd/director/main.go` lines 83–97](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/cmd/director/main.go#L83-L97)). That's how the demo recovers when the in-memory dev server restarts.

**Docs:** [Entity Workflow pattern](https://docs.temporal.io/design-patterns/entity-workflow), [Continue-As-New pattern](https://docs.temporal.io/design-patterns/continue-as-new)

### Survive a deleted pod with a retry policy

**When to use it:** after beat 4, or when someone asks what happened to the orders on the pod you deleted.

**Where it is:** [`internal/orders/workflow.go` lines 37–75](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/internal/orders/workflow.go#L37-L75) (`code -g internal/orders/workflow.go:37`)

```go
func OrderWorkflow(ctx workflow.Context, order Order) (Result, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        500 * time.Millisecond,
			BackoffCoefficient:     2,
			MaximumInterval:        5 * time.Second,
			MaximumAttempts:        5,
			NonRetryableErrorTypes: []string{ErrOutOfStock},
		},
	})
	// ...
	var tracking string
	if err := workflow.ExecuteActivity(ctx, a.ShipOrder, order).Get(ctx, &tracking); err != nil {
		var appErr *temporal.ApplicationError
		if !errors.As(err, &appErr) {
			return res, err
		}
		// Compensate: refund and put the stock back.
		_ = workflow.ExecuteActivity(ctx, a.RefundPayment, order).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, a.ReleaseInventory, order).Get(ctx, nil)
		res.Status = "refunded"
	} else {
		// ...
	}
	// ...
}
```

**What you need to understand:**

- When a pod is deleted, Temporal retries each Activity that was running there on another pod of the same version. If the pod can't report the failure, the 30-second `StartToCloseTimeout` sets how long Temporal waits before the retry.
- The Workflow's own progress is in Temporal, not in the pod. Steps that already finished, such as the payment, don't run again.
- The same retry policy handles the 3% of Activity calls that fail on purpose. Each retry waits longer, from half a second up to 5 seconds, for at most 5 attempts.
- `OutOfStock` is non-retryable, because waiting won't bring stock back. The Workflow then refunds the payment and releases the stock: the Saga pattern.
- On a normal scale-in, the Worker gets `SIGTERM` and calls `w.Stop()`, which stops it polling for new tasks ([`cmd/worker/main.go` lines 101–106](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/cmd/worker/main.go#L101-L106)), and Kubernetes allows it 30 seconds to exit.
- The customer doesn't write recovery code, a job table, or a checkpoint. The retry policy is the whole configuration.

**Docs:** [Retry policies](https://docs.temporal.io/encyclopedia/retry-policies), [Saga pattern](https://docs.temporal.io/design-patterns/saga-pattern)

### What's demo-only

Tell the audience which parts a customer would leave out:

- **Simulated work and injected failures.** The Activities sleep for a few hundred milliseconds to about a second and a half. 3% of calls fail with a retryable error, and 2% of shipments fail as out of stock ([`internal/orders/activities.go` lines 33–48](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/internal/orders/activities.go#L33-L48) and [lines 66–77](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/internal/orders/activities.go#L66-L77)). Set `FAILURE_RATE` on the Worker to change the first.
- **The traffic director and its rollout patch.** See the two entries above.
- **Fast version drainage.** The dev server starts with shorter drainage checks, so a drained version is cleaned up in about a minute instead of several ([`scripts/booth.sh` lines 70–74](https://github.com/mikeacjones/temporal-demo-k8s-scaling/blob/838fe9ccddad700efb67fff42881f22d08b8ce2d/scripts/booth.sh#L70-L74)). On Temporal Cloud, expect old versions to stay listed for longer.
- **The Worker Controller build.** The demo pins an unreleased build from `main` for #616. See [Scale each version on its own backlog and slot use](#scale-each-version-on-its-own-backlog-and-slot-use).
- **Unauthenticated staff controls.** The dashboard is reachable only on `127.0.0.1`, through a port-forward, because its control endpoints have no authentication.

## Reset and tear down

Between runs you don't need to reset anything. The cycle repeats forever. If you paused the traffic, press **Resume**.

To start over from the idle phase with the default traffic curve, terminate the director. Its Worker starts a fresh one within 15 seconds, and orders already running finish normally:

```bash
temporal --disable-config-file --address localhost:7233 workflow terminate \
  --workflow-id traffic-director --reason "reset demo"
```

When you're done:

- `make stop` stops the dev server, the port-forward, and the cluster guard. The cluster keeps running.
- `make down` stops those and the minikube cluster. The next `make up` resumes it quickly.
- `make reset` deletes the minikube cluster entirely.

Nothing in the demo runs on Temporal Cloud, so stopping it leaves nothing that uses Actions.

## When something goes wrong

| Symptom | Likely cause | Fix |
|---|---|---|
| <http://localhost:8080> doesn't load | The port-forward hung when the dashboard pod was replaced | Wait about 20 seconds; the supervisor restarts it. If it doesn't, run `make stop`, then `make booth` |
| The dashboard says "No pods yet. Is the cluster up?" | Docker Desktop or minikube stopped, for example after the laptop slept | Wait a minute; the cluster guard restarts both. Otherwise run `make up` |
| `make up` fails because port 7233 is in use | Another Temporal dev server is running | Use [a different Temporal port](#use-a-different-temporal-port) |
| New pods stay `Pending` in k9s | The cluster is out of CPU or memory | Give Docker Desktop more resources, then `make reset` and `make up` with a larger `CPUS` or `MEMORY` |
| **Worker versions** shows "last rollout failed" | The director couldn't patch the WorkerDeployment | Run `kubectl --context temporal-demo -n demo logs deploy/traffic-director` and look for `rollout failed` |
| An old version stays `draining` | It still has running orders, or Temporal hasn't rechecked drainage yet | Wait about a minute. Check with `temporal --disable-config-file --address localhost:7233 worker deployment describe --name demo/order-worker` |
| A drained version keeps its pods | The Worker Controller build lacks #616, so every version scales on the whole deployment's slot use | Check `image.tag` in `deploy/helm/worker-controller-values.yaml`, then run `make platform` |
| **Slot utilization** stays at 0% under load | Prometheus isn't scraping the Workers | Run `kubectl --context temporal-demo -n monitoring get pods`, and rerun `make platform` if Prometheus isn't ready |
| Rebuilding the dashboard started a rollout | `make images deploy` gives every image a new tag | Use `make dashboard` to rebuild only the dashboard |

## Run it locally

Today this is the only way to run the demo. The sections above already use the local URLs and commands. This section collects what you need to set it up and change it.

### Tools and versions

- macOS or Linux, with Docker Desktop given at least 8 GB of memory and 6 CPUs
- `minikube`, `kubectl`, and `helm`
- Go 1.27 or later
- The [Temporal CLI](https://docs.temporal.io/cli)

`make up` installs cert-manager, KEDA, Prometheus, and the Temporal Worker Controller into minikube at the versions pinned in the `Makefile`.

### Commands and URLs

| Command | What it does |
|---|---|
| `make up` | Starts the dev server, minikube, the cluster add-ons, the images, the workloads, and the dashboard |
| `make status` | Lists the WorkerDeployment, WorkerResourceTemplate, ScaledObjects, HPAs, Deployments, and pods |
| `make logs` | Follows the host-side logs in `.run/` |
| `make k9s` | Opens a full-size k9s in this terminal, running inside the dashboard pod |
| `make k9s-window` | Opens the same k9s in a new Terminal window (macOS) |
| `make dashboard` | Rebuilds and restarts only the dashboard, without a Worker rollout |
| `make images deploy` | Rebuilds every image and applies the manifests, which rolls out a new version |
| `make test` | Runs the unit tests |

- Dashboard: <http://localhost:8080>. Add `?kiosk=1` for a big screen, `?k9s=1` to open with the k9s panel, and `?controls=0` to hide the staff controls.
- Temporal UI: <http://localhost:8233>

The demo's binaries and commands ignore your Temporal CLI config file, so a default profile that points at Temporal Cloud can't redirect it. Pass `--disable-config-file` when you run `temporal` commands against the demo yourself.

### Use a different Temporal port

If another dev server already uses port 7233, give the demo a different one:

```bash
make up TEMPORAL_PORT=7243
```

The Temporal UI is then on port 8243. Use `--address localhost:7243` in the `temporal` commands in this runbook.

### Rehearse in less time

The default cycle takes about 5 minutes, and an automatic rollout comes every third cycle. To see every beat in under 5 minutes, shorten the cycle and roll out every cycle:

```bash
temporal --disable-config-file --address localhost:7233 workflow update execute \
  --workflow-id traffic-director --name set-config \
  --input '{"idleSeconds":15,"rampUpSeconds":45,"peakSeconds":45,"rampDownSeconds":45,"cooldownSeconds":15,"baseRate":0.6,"peakRate":10,"tickSeconds":2,"rolloutEveryNCycles":1}'
```

Terminate the director, as in [Reset and tear down](#reset-and-tear-down), to go back to the defaults.

### Tuning

| Setting | Where | Default |
|---|---|---|
| Activity slots per pod | `ACTIVITY_SLOTS` in `deploy/k8s/order-worker.yaml` | 4 |
| Traffic curve and rollout cadence | `DefaultConfig()` in `internal/director/profile.go`, or live with the `set-config` Update above | 0.6 to 10 orders/s; 30, 90, 60, 90, and 30 seconds; a rollout every 3 cycles |
| Scaling triggers and speed | `deploy/k8s/order-worker-autoscaling.yaml` | 5 waiting tasks per pod, 70% slot use, at least 1 pod |
| Rollout steps and cleanup | `deploy/k8s/order-worker.yaml` | 25% and 50% for 45 seconds each; scale to zero after 30 seconds; delete after 2 minutes |
| Activity speed | `SPEED` on the Worker | 1 (2 halves every duration) |
| Injected failure rate | `FAILURE_RATE` on the Worker | 0.03 |
| Pod deletion from the k9s panel | `K9S_ALLOW_DELETE` in `deploy/k8s/dashboard.yaml` | `"true"` |

## Keeping this runbook current

When you change the code behind a beat, update its walkthrough step and its "Show it in code" entry together, and refresh the permalink. Before an event, list the linked files that changed since their permalink was made:

```sh
grep -oE 'blob/[0-9a-f]{40}/[^#)]+' RUNBOOK.md | sort -u | while IFS=/ read -r _ sha file; do
  git diff --quiet "$sha" HEAD -- "$file" || echo "changed since ${sha:0:7}: $file"
done
```

A file on the list may only have moved lines. Check the snippet against the current code, then refresh the link.
