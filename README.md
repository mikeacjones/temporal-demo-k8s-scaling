# Temporal Worker Autoscaling on Kubernetes

A self-running booth demo of **Temporal workers that scale themselves** on Kubernetes. It runs on a laptop, written in Go, with:

- the [Temporal Worker Controller](https://github.com/temporalio/temporal-worker-controller) managing **versioned** workers (Worker Versioning, progressive rollouts, pinned workflows)
- **KEDA**, one `ScaledObject` per worker version (rendered by a `WorkerResourceTemplate`), scaling on the version's **task queue backlog** *and* **slot utilization**
- a **traffic director** entity workflow on its own worker that cycles traffic up and down forever and rolls out a new worker version every few cycles
- a dashboard in the temporal.io look (starfield, Ziggy) and `k9s` to watch the pods

![architecture](docs/architecture.svg)

## How it works

| Piece | What it does |
|---|---|
| `cmd/director` | Dedicated worker (task queue `traffic-director`) for the `TrafficDirector` entity workflow. Every 2s it starts a batch of `OrderWorkflow`s sized by a ~5 minute curve: idle → ramp up → peak → ramp down → cooldown. It continues-as-new each cycle. Every 3rd cycle it patches the `WorkerDeployment`'s pod-template annotation, which creates a new Build ID and starts a progressive rollout. Signals: `pause`, `resume`, `surge`, `rollout-now`. Update: `set-config`. Query: `state`. |
| `cmd/worker` | The order worker (task queue `orders`). Each pod gets only **4 activity slots**, so traffic forces scale-out. It uses poller autoscaling and Worker Versioning (`Pinned`), connects with the env vars the controller injects, and exports SDK metrics labelled per version. |
| `WorkerDeployment` | `Progressive` rollout (25% → 50% → 100%) with short sunset delays, so drained versions disappear quickly. |
| `WorkerResourceTemplate` | Renders a KEDA `ScaledObject` per Build ID with two triggers: `temporal` (that version's backlog; the controller injects the deployment/build ID) and `prometheus` (`temporal_slot_utilization`). KEDA uses whichever wants more pods. |
| `cmd/web` | Dashboard on the host. It reads Temporal (director query, deployment routing, per-version backlog), Kubernetes (pods, replicas) and Prometheus (slots per pod), and streams them over SSE. |

The design follows Temporal's [scaling recommendations](https://github.com/temporalio/temporal-worker-controller/blob/main/docs/scaling-recommendations.md) for self-hosted or local servers:
- **Backlog** reacts fast.
- **Slot utilization** keeps busy pods from being scaled in just because the queue is momentarily empty.
- `minReplicaCount: 1` keeps every version polling, which rollouts need.

## Prerequisites

macOS or Linux with: Docker (Desktop) with **≥ 8 GB** and 6 CPUs available, `minikube`, `kubectl`, `helm`, `go` ≥ 1.27, the `temporal` CLI, and `k9s`.

## Run it

```bash
make up          # dev server + minikube + cert-manager, Worker Controller, KEDA, Prometheus + images + workloads + dashboard
make k9s         # in another terminal: watch pods come and go
```

- Dashboard: <http://localhost:8080> (staff controls: `?controls=1`, big screen: `?kiosk=1`)
- Temporal UI: <http://localhost:8233>

The first run takes a few minutes while it pulls charts and images. After that, `make up` is idempotent and quick.

The host-side processes (dev server, Prometheus port-forward, dashboard) run under `scripts/booth.sh`, which restarts any of them if they die. The dev server runs **in memory**, so a restart wipes Temporal's state. That's fine: the director recreates its entity workflow within 15 seconds, and the Worker Controller re-registers the current version. Completed orders expire after 1 hour (the director enforces namespace retention), so memory stays flat all day.

| Command | |
|---|---|
| `make stop` | Stop the host-side processes |
| `make down` | Stop host processes and the cluster (keeps it for a fast `make up`) |
| `make reset` | Delete the minikube cluster |
| `make status` | WorkerDeployment / WRT / ScaledObject / HPA / pods |
| `make logs` | Tail the host-side logs in `.run/` |
| `make deploy` | Re-apply manifests. With new images (`make images deploy`) this is a manual rollout. |
| `make test` | Unit tests |

> **Already running a dev server on 7233?** Use another port for the demo: `make up TEMPORAL_PORT=7243` (the UI is then on 8243).

## Booth checklist

1. Give Docker Desktop ≥ 8 GB RAM. Turn off sleep and the screen saver.
2. `make up`, then open `http://localhost:8080/?kiosk=1` full screen. Put `k9s` (`make k9s`) on a second screen.
3. Keep a staff tab at `?controls=1` for **Surge ⚡** (extra traffic for 60s), **Pause**, and **Roll out version**.
4. The dashboard only listens on `127.0.0.1` because the control endpoints are unauthenticated. To expose it, set `WEB_ADDR=0.0.0.0:8080`, but only on a trusted network.

## Tuning

| Knob | Where | Default |
|---|---|---|
| Activity slots per pod | `ACTIVITY_SLOTS` in `deploy/k8s/order-worker.yaml` | 4 |
| Base / peak orders/s, phase lengths, rollout cadence | `DefaultConfig()` in `internal/director/profile.go`, or live via the `set-config` update | 0.6 / 10 /s, 30-90-60-90-30s, every 3 cycles |
| Min / max pods, triggers, HPA behavior | `deploy/k8s/order-worker-autoscaling.yaml` | 1 / 10, backlog 5 per pod, slots 70% |
| Rollout steps and sunset | `deploy/k8s/order-worker.yaml` | 25% / 50% for 45s each; scale-down 30s, delete 2m |
| Activity speed | `SPEED` env on the worker | 1 |

Change the director's config live without redeploying:

```bash
temporal --disable-config-file --address localhost:7233 workflow update execute \
  --workflow-id traffic-director --name set-config \
  --input '{"idleSeconds":30,"rampUpSeconds":60,"peakSeconds":60,"rampDownSeconds":60,"cooldownSeconds":30,"baseRate":0.6,"peakRate":12,"tickSeconds":2,"rolloutEveryNCycles":2}'
```

## Notes

- **Per-version slot trigger.** Worker Controller v1.12.0 doesn't substitute `{{temporal_worker_build_id}}` tokens inside ScaledObjects yet ([#616](https://github.com/temporalio/temporal-worker-controller/pull/616) is merged but unreleased). For now the Prometheus trigger reads the whole WorkerDeployment's utilization, and the backlog trigger is already per version. The per-version query is in the manifest, commented out, ready to switch on.
- **Fast drainage.** The dev server is started with shorter version-drainage checks (`matching.wv.VersionDrainageStatus*`), so drained versions sunset within about a minute instead of several.
- **Your Temporal CLI profile is ignored.** The binaries and scripts ignore the Temporal CLI config file, so a default profile pointing at Temporal Cloud can't redirect the demo.
- **Fonts and images are bundled.** Aeonik, temporal.io's typeface, is commercial, so the dashboard uses Manrope with Noto Sans Mono (both OFL, bundled under `web/static/fonts`). The Temporal logo and Ziggy are Temporal brand assets.
