# Temporal Worker Controller + KEDA autoscaling booth demo.
#
#   make up      # everything: cluster, platform, images, workloads, dev server, dashboard
#   make demo    # kill everything (cluster included) and relaunch from scratch
#   make k9s     # full-screen k9s (runs inside the cluster); make k9s-window pops it out
#   make down    # stop host processes and the cluster
#
SHELL := /bin/bash
PROFILE        ?= temporal-demo
TEMPORAL_PORT  ?= 7233
UI_PORT        := $(shell echo $$(( $(TEMPORAL_PORT) + 1000 )))
# Evaluated once per make run (:=) so images and manifests share one tag.
ifndef TAG
TAG := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)$(shell git diff --quiet 2>/dev/null || echo -$$(date +%s))
endif
KUBE_CONTEXT   := kind-$(PROFILE)
NODE           := $(PROFILE)-control-plane
KUBECTL        := kubectl --context $(KUBE_CONTEXT)
HELM           := helm --kube-context $(KUBE_CONTEXT)

CONTROLLER_CHART_VERSION ?= 0.31.0
KEDA_CHART_VERSION       ?= 2.21.0
CERT_MANAGER_VERSION     ?= v1.21.2
PROMETHEUS_CHART_VERSION ?= 29.36.1

.PHONY: up demo tools cluster platform images dashboard deploy booth booth-fg stop down reset k9s k9s-window status logs test web record

up: tools booth cluster platform images deploy status ## Bring up the whole demo
	@echo; echo "Dashboard:   http://localhost:8080   (k9s panel: press k, or ?k9s=1; staff controls: s to collapse, ?controls=0 to hide)"
	@echo "Temporal UI: http://localhost:$(UI_PORT)"
	@echo "k9s window:  make k9s-window   (or make k9s in this terminal)"

demo: ## Kill everything, delete the cluster, and relaunch from scratch
	@$(MAKE) --no-print-directory stop
	@# Leftovers from a booth whose pid file is gone; supervisors first so nothing respawns.
	-@pkill -f "scripts/booth.sh"
	-@pkill -f "temporal server start-dev --ip 0.0.0.0 --port $(TEMPORAL_PORT) "
	-@pkill -f "kubectl --context $(KUBE_CONTEXT) -n demo port-forward"
	kind delete cluster --name $(PROFILE)
	@# Create the cluster before the booth starts, or its cluster guard races this start with its own.
	$(MAKE) --no-print-directory cluster
	$(MAKE) --no-print-directory up

tools:
	@for t in go docker kind kubectl helm temporal curl; do command -v $$t >/dev/null || { echo "missing: $$t"; exit 1; }; done
	@docker info >/dev/null 2>&1 || { echo "Docker is not running"; exit 1; }

cluster: ## Create (or restart) the kind cluster; config in deploy/kind/cluster.yaml
	@if ! kind get clusters 2>/dev/null | grep -qx '$(PROFILE)'; then \
		kind create cluster --name $(PROFILE) --config deploy/kind/cluster.yaml --wait 120s; \
	elif [ "$$(docker inspect -f '{{.State.Running}}' $(NODE) 2>/dev/null)" != true ]; then \
		echo "starting stopped kind node $(NODE)"; docker start $(NODE) >/dev/null; \
	fi
	@kubectl config get-contexts $(KUBE_CONTEXT) >/dev/null 2>&1 || kind export kubeconfig --name $(PROFILE)
	@for _ in $$(seq 1 60); do $(KUBECTL) get --raw /readyz >/dev/null 2>&1 && exit 0; sleep 2; done; \
		echo "kind API server did not become ready"; exit 1

platform: ## cert-manager, Worker Controller, KEDA, Prometheus
	helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
	helm repo add kedacore https://kedacore.github.io/charts --force-update >/dev/null
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
	$(HELM) upgrade --install cert-manager jetstack/cert-manager -n cert-manager --create-namespace \
		--version $(CERT_MANAGER_VERSION) --set crds.enabled=true --wait
	@# After a node restart, pods report stale readiness and helm --wait passes before
	@# cert-manager's webhook answers. Probe it with a server-side dry run until it does.
	@for _ in $$(seq 1 60); do \
		printf 'apiVersion: cert-manager.io/v1\nkind: Issuer\nmetadata: {name: probe, namespace: cert-manager}\nspec: {selfSigned: {}}\n' \
			| $(KUBECTL) create --dry-run=server -f - >/dev/null 2>&1 && exit 0; sleep 3; \
	done; echo "cert-manager webhook not answering"; exit 1
	$(HELM) upgrade --install keda kedacore/keda -n keda --create-namespace \
		--version $(KEDA_CHART_VERSION) -f deploy/helm/keda-values.yaml --wait
	$(HELM) upgrade --install prometheus prometheus-community/prometheus -n monitoring --create-namespace \
		--version $(PROMETHEUS_CHART_VERSION) -f deploy/helm/prometheus-values.yaml --wait
	$(HELM) upgrade --install temporal-worker-controller-crds oci://docker.io/temporalio/temporal-worker-controller-crds \
		-n temporal-system --create-namespace --version $(CONTROLLER_CHART_VERSION) --wait
	$(HELM) upgrade --install temporal-worker-controller oci://docker.io/temporalio/temporal-worker-controller \
		-n temporal-system --version $(CONTROLLER_CHART_VERSION) -f deploy/helm/worker-controller-values.yaml --wait

images: ## Build worker images locally and load them into the kind node
	docker build --build-arg BIN=worker -t order-worker:$(TAG) .
	docker build --build-arg BIN=director -t traffic-director:$(TAG) .
	docker build --build-arg BIN=web --target web -t dashboard:$(TAG) .
	kind load docker-image --name $(PROFILE) order-worker:$(TAG) traffic-director:$(TAG) dashboard:$(TAG)

dashboard: ## Rebuild and roll just the dashboard (no worker rollout)
	docker build --build-arg BIN=web --target web -t dashboard:$(TAG) .
	kind load docker-image --name $(PROFILE) dashboard:$(TAG)
	sed -e 's/dashboard:dev/dashboard:$(TAG)/' \
	    -e 's|http://localhost:8233|http://localhost:$(UI_PORT)|' deploy/k8s/dashboard.yaml \
		| $(KUBECTL) -n demo apply -f -
	$(KUBECTL) -n demo rollout status deploy/dashboard --timeout=120s

deploy: ## Apply workloads (a new TAG => new Build ID => progressive rollout)
	@# pipefail: a failed endpoint lookup must fail the apply. Retries cover admission
	@# webhooks (Worker Controller, KEDA) that are still starting after a node restart.
	@set -o pipefail; for _ in 1 2 3 4 5 6; do \
		{ $(KUBECTL) kustomize deploy/k8s \
			| sed -e 's/order-worker:dev/order-worker:$(TAG)/' -e 's/traffic-director:dev/traffic-director:$(TAG)/' \
			      -e 's/dashboard:dev/dashboard:$(TAG)/' \
			      -e 's|http://localhost:8233|http://localhost:$(UI_PORT)|'; \
		  echo ---; scripts/dev-server-endpoint.sh $(NODE) $(TEMPORAL_PORT); } \
		| $(KUBECTL) apply -f - && exit 0; \
		echo "apply failed; retrying in 5s"; sleep 5; \
	done; exit 1

booth: ## Start dev server + dashboard port-forward + cluster guard under a supervisor (background)
	@mkdir -p .run
	@if [ -f .run/booth.pid ] && kill -0 $$(cat .run/booth.pid) 2>/dev/null; then echo "booth already running"; \
	else TEMPORAL_PORT=$(TEMPORAL_PORT) PROFILE=$(PROFILE) KUBE_CONTEXT=$(KUBE_CONTEXT) nohup scripts/booth.sh > .run/booth.log 2>&1 & sleep 3; cat .run/booth.log; fi

booth-fg: ## Same as booth, in the foreground
	TEMPORAL_PORT=$(TEMPORAL_PORT) PROFILE=$(PROFILE) KUBE_CONTEXT=$(KUBE_CONTEXT) scripts/booth.sh

stop: ## Stop the host-side processes
	@if [ -f .run/booth.pid ]; then kill $$(cat .run/booth.pid) 2>/dev/null; rm -f .run/booth.pid; echo stopped; fi

down: stop ## Stop everything (the kind node is kept; `make up` resumes it)
	-docker stop $(NODE)

reset: stop ## Delete the cluster entirely
	kind delete cluster --name $(PROFILE)

k9s: ## Full-screen k9s, run inside the dashboard pod (isolated, read-only)
	$(KUBECTL) -n demo exec -it deploy/dashboard -- /app k9s

k9s-window: ## Pop k9s out into its own terminal window (macOS Terminal; Linux gnome-terminal/x-terminal-emulator)
	@if [ "$$(uname)" = Darwin ]; then \
		osascript -e 'tell application "Terminal" to do script "cd $(CURDIR) && make k9s PROFILE=$(PROFILE)"' -e 'tell application "Terminal" to activate'; \
	elif command -v gnome-terminal >/dev/null; then \
		gnome-terminal -- bash -c 'cd "$(CURDIR)" && make k9s PROFILE=$(PROFILE)'; \
	elif command -v x-terminal-emulator >/dev/null; then \
		x-terminal-emulator -e bash -c 'cd "$(CURDIR)" && make k9s PROFILE=$(PROFILE)'; \
	else echo "no terminal emulator found; run 'make k9s' in a terminal instead"; fi

status:
	@$(KUBECTL) -n demo get workerdeployment,wrt,scaledobject,hpa,deploy,pods 2>/dev/null || true

logs:
	tail -n 50 -f .run/*.log

web: ## Run the dashboard locally for development (no k9s panel outside the cluster)
	TEMPORAL_ADDRESS=localhost:$(TEMPORAL_PORT) TEMPORAL_UI_URL=http://localhost:$(UI_PORT) KUBE_CONTEXT=$(KUBE_CONTEXT) go run ./cmd/web -addr 127.0.0.1:8081

test:
	go test ./...

record: ## Record one full traffic cycle into docs/demo.{mp4,gif} (needs `make up` running)
	go run -C tools/record . -url http://127.0.0.1:8080 -out $(CURDIR)/docs
