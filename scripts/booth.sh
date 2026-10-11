#!/usr/bin/env bash
# Supervises the host-side pieces of the booth demo and restarts any that die:
#   - Temporal dev server (in memory; the director re-creates its entity on restart)
#   - kubectl port-forward to Prometheus (for the dashboard's slot view)
#   - the dashboard web server
# Logs go to .run/*.log. Stop with: make down (or kill the PID in .run/booth.pid).
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p .run bin
echo $$ > .run/booth.pid

PROFILE="${PROFILE:-temporal-demo}"
WEB_ADDR="${WEB_ADDR:-:8080}"
PROM_PORT="${PROM_PORT:-9091}"
TEMPORAL_PORT="${TEMPORAL_PORT:-7233}"
UI_PORT=$((TEMPORAL_PORT + 1000))

go build -o bin/web ./cmd/web || exit 1

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

supervise temporal temporal server start-dev \
  --ip 0.0.0.0 --port "$TEMPORAL_PORT" --ui-ip 127.0.0.1 --ui-port "$UI_PORT" \
  --log-level warn --ui-disable-news-fetch

supervise port-forward kubectl --context "$PROFILE" -n monitoring \
  port-forward svc/prometheus-server "$PROM_PORT:80"

supervise web env TEMPORAL_ADDRESS="localhost:$TEMPORAL_PORT" TEMPORAL_NAMESPACE=default KUBE_CONTEXT="$PROFILE" PROMETHEUS_URL="http://localhost:$PROM_PORT" \
  WEB_ADDR="$WEB_ADDR" ./bin/web

echo "booth supervisor running (pid $$). Dashboard: http://localhost${WEB_ADDR}  Temporal UI: http://localhost:$UI_PORT"
wait
