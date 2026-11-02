#!/usr/bin/env bash
# Prints the EndpointSlice that points the temporal-dev-server Service
# (deploy/k8s/dev-server.yaml) at the dev server on the host.
#
# Candidates for the host's address, as seen from the kind node:
#   - host.docker.internal: Docker Desktop (macOS, Linux).
#   - the kind network's gateway: Docker Engine (Linux), where the bridge is the host.
# The first one where the dev server's port answers wins. If none answers
# (dev server not up yet), the first candidate is used.
set -euo pipefail
NODE="${1:?usage: $0 NODE_CONTAINER TEMPORAL_PORT}"
PORT="${2:?usage: $0 NODE_CONTAINER TEMPORAL_PORT}"

candidates=()
ip=$(docker exec "$NODE" getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR==1 {print $1}' || true)
[ -n "$ip" ] && candidates+=("$ip")
ip=$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' 2>/dev/null \
  | tr ' ' '\n' | grep -E '^[0-9]+(\.[0-9]+){3}$' | head -n1 || true)
[ -n "$ip" ] && candidates+=("$ip")
[ ${#candidates[@]} -gt 0 ] || { echo "could not find the host's address from $NODE" >&2; exit 1; }

chosen=""
for _ in $(seq 1 10); do
  for ip in "${candidates[@]}"; do
    if docker exec "$NODE" timeout 2 bash -c "</dev/tcp/$ip/$PORT" 2>/dev/null; then chosen=$ip; break 2; fi
  done
  sleep 1
done
if [ -z "$chosen" ]; then
  chosen=${candidates[0]}
  echo "warning: dev server not reachable on :$PORT at ${candidates[*]}; using $chosen" >&2
fi

cat <<YAML
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: temporal-dev-server
  namespace: demo
  labels:
    kubernetes.io/service-name: temporal-dev-server
addressType: IPv4
ports:
  - name: grpc
    port: $PORT
endpoints:
  - addresses: ["$chosen"]
YAML
