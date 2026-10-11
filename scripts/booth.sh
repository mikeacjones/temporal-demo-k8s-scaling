#!/usr/bin/env bash
# Supervises the host-side pieces of the booth demo and restarts any that die:
#   - Temporal dev server (in memory; the director re-creates its entity on restart)
#   - kubectl port-forward to the in-cluster dashboard (localhost:8080)
#   - a cluster guard that restarts Docker Desktop / minikube if they stop
# Logs go to .run/*.log. Stop with: make stop (or kill the PID in .run/booth.pid).
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p .run
echo $$ > .run/booth.pid

PROFILE="${PROFILE:-temporal-demo}"
WEB_PORT="${WEB_PORT:-8080}"
TEMPORAL_PORT="${TEMPORAL_PORT:-7233}"
UI_PORT=$((TEMPORAL_PORT + 1000))

pids=()
cleanup() {
  trap - INT TERM EXIT
  for p in "${pids[@]}"; do pkill -P "$p" 2>/dev/null; kill "$p" 2>/dev/null; done
  rm -f .run/booth.pid
  exit 0
}
trap cleanup INT TERM EXIT

# supervise NAME CMD... : run CMD forever, restarting with a short backoff.
supervise() {
  local name=$1; shift
  (
    while true; do
      echo "[$(date '+%F %T')] starting $name" >> ".run/$name.log"
      "$@" >> ".run/$name.log" 2>&1
      echo "[$(date '+%F %T')] $name exited ($?); restarting in 3s" >> ".run/$name.log"
      sleep 3
    done
  ) &
  pids+=($!)
}

# cluster_guard: Docker Desktop quitting (or the laptop sleeping) takes the
# whole cluster down. Bring both back instead of leaving a dead booth screen.
cluster_guard() {
  local tick=0 misses=0
  while true; do
    if (( tick % 3 == 0 )); then
      if ! docker info >/dev/null 2>&1; then
        echo "[$(date '+%F %T')] docker is down; starting Docker Desktop"
        [ "$(uname)" = Darwin ] && open -ga Docker
        for _ in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 5; done
      fi
      if docker info >/dev/null 2>&1 && ! minikube -p "$PROFILE" status --format '{{.APIServer}}' 2>/dev/null | grep -q Running; then
        echo "[$(date '+%F %T')] minikube is down; starting it"
        minikube start -p "$PROFILE"
      fi
    fi
    # kubectl port-forward can hang (instead of exiting) when its pod is
    # replaced; if the dashboard stops answering, kill it so it restarts.
    if curl -fsS -m 3 "http://127.0.0.1:$WEB_PORT/api/info" >/dev/null 2>&1; then
      misses=0
    elif (( ++misses >= 2 )); then
      echo "[$(date '+%F %T')] dashboard not answering; restarting port-forward"
      pkill -f "port-forward --address 127.0.0.1 svc/dashboard" || true
      misses=0
    fi
    tick=$((tick + 1))
    sleep 10
  done
}

supervise temporal temporal server start-dev \
  --ip 0.0.0.0 --port "$TEMPORAL_PORT" --ui-ip 127.0.0.1 --ui-port "$UI_PORT" \
  --log-level warn --ui-disable-news-fetch \
  --dynamic-config-value matching.wv.VersionDrainageStatusVisibilityGracePeriod='"30s"' \
  --dynamic-config-value matching.wv.VersionDrainageStatusRefreshInterval='"10s"'

# Localhost only: the dashboard's staff controls are unauthenticated.
supervise dashboard kubectl --context "$PROFILE" -n demo \
  port-forward --address 127.0.0.1 svc/dashboard "$WEB_PORT:80"

supervise cluster-guard cluster_guard

echo "booth supervisor running (pid $$). Dashboard: http://localhost:$WEB_PORT  Temporal UI: http://localhost:$UI_PORT"
wait
