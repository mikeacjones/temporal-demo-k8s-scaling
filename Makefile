# Temporal Worker Controller + KEDA autoscaling booth demo.
#
#   make up      # everything: cluster, platform, images, workloads, dev server, dashboard
#   make k9s     # watch the pods
#   make down    # stop host processes and the cluster
#
SHELL := /bin/bash
PROFILE        ?= temporal-demo
CPUS           ?= 6
MEMORY         ?= 6g
TEMPORAL_PORT  ?= 7233
# Evaluated once per make run (:=) so images and manifests share one tag.
ifndef TAG
TAG := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)$(shell git diff --quiet 2>/dev/null || echo -$$(date +%s))
endif
KUBECTL        := kubectl --context $(PROFILE)
HELM           := helm --kube-context $(PROFILE)

CONTROLLER_CHART_VERSION ?= 0.31.0
KEDA_CHART_VERSION       ?= 2.21.0
CERT_MANAGER_VERSION     ?= v1.21.2
PROMETHEUS_CHART_VERSION ?= 29.36.1

.PHONY: up tools cluster platform images deploy booth booth-fg stop down reset k9s status logs test web

up: tools booth cluster platform images deploy status ## Bring up the whole demo
	@echo; echo "Dashboard:   http://localhost:8080   (staff controls: http://localhost:8080/?controls=1)"
	@echo "Temporal UI: http://localhost:$$(( $(TEMPORAL_PORT) + 1000 ))"
	@echo "Pods:        make k9s"

tools:
	@for t in go docker minikube kubectl helm temporal k9s; do command -v $$t >/dev/null || { echo "missing: $$t"; exit 1; }; done
	@docker info >/dev/null 2>&1 || { echo "Docker is not running"; exit 1; }

cluster: ## Start minikube (HPA syncs every 5s so scaling is snappy)
	@minikube status -p $(PROFILE) >/dev/null 2>&1 || minikube start -p $(PROFILE) --driver=docker \
		--cpus=$(CPUS) --memory=$(MEMORY) \
		--extra-config=controller-manager.horizontal-pod-autoscaler-sync-period=5s

platform: ## cert-manager, Worker Controller, KEDA, Prometheus
	helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
	helm repo add kedacore https://kedacore.github.io/charts --force-update >/dev/null
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
	$(HELM) upgrade --install cert-manager jetstack/cert-manager -n cert-manager --create-namespace \
		--version $(CERT_MANAGER_VERSION) --set crds.enabled=true --wait
	$(HELM) upgrade --install keda kedacore/keda -n keda --create-namespace \
		--version $(KEDA_CHART_VERSION) -f deploy/helm/keda-values.yaml --wait
	$(HELM) upgrade --install prometheus prometheus-community/prometheus -n monitoring --create-namespace \
		--version $(PROMETHEUS_CHART_VERSION) -f deploy/helm/prometheus-values.yaml --wait
	$(HELM) upgrade --install temporal-worker-controller-crds oci://docker.io/temporalio/temporal-worker-controller-crds \
		-n temporal-system --create-namespace --version $(CONTROLLER_CHART_VERSION) --wait
	$(HELM) upgrade --install temporal-worker-controller oci://docker.io/temporalio/temporal-worker-controller \
		-n temporal-system --version $(CONTROLLER_CHART_VERSION) -f deploy/helm/worker-controller-values.yaml --wait

images: ## Build worker images locally and load them into minikube
	docker build --build-arg BIN=worker -t order-worker:$(TAG) .
	docker build --build-arg BIN=director -t traffic-director:$(TAG) .
	minikube -p $(PROFILE) image load order-worker:$(TAG)
	minikube -p $(PROFILE) image load traffic-director:$(TAG)

deploy: ## Apply workloads (a new TAG => new Build ID => progressive rollout)
	$(KUBECTL) kustomize deploy/k8s \
		| sed -e 's/order-worker:dev/order-worker:$(TAG)/' -e 's/traffic-director:dev/traffic-director:$(TAG)/' \
		      -e 's/host.minikube.internal:7233/host.minikube.internal:$(TEMPORAL_PORT)/' \
		| $(KUBECTL) apply -f -

booth: ## Start dev server + Prometheus port-forward + dashboard under a supervisor (background)
	@mkdir -p .run
	@if [ -f .run/booth.pid ] && kill -0 $$(cat .run/booth.pid) 2>/dev/null; then echo "booth already running"; \
	else TEMPORAL_PORT=$(TEMPORAL_PORT) PROFILE=$(PROFILE) nohup scripts/booth.sh > .run/booth.log 2>&1 & sleep 3; cat .run/booth.log; fi

booth-fg: ## Same as booth, in the foreground
	TEMPORAL_PORT=$(TEMPORAL_PORT) PROFILE=$(PROFILE) scripts/booth.sh

stop: ## Stop the host-side processes
	@if [ -f .run/booth.pid ]; then kill $$(cat .run/booth.pid) 2>/dev/null; rm -f .run/booth.pid; echo stopped; fi

down: stop ## Stop everything (cluster is kept on disk; `make up` resumes)
	minikube stop -p $(PROFILE)

reset: stop ## Delete the cluster entirely
	minikube delete -p $(PROFILE)

k9s: ## Watch the demo namespace
	k9s --context $(PROFILE) -n demo -c pods

status:
	@$(KUBECTL) -n demo get workerdeployment,wrt,scaledobject,hpa,deploy,pods 2>/dev/null || true

logs:
	tail -n 50 -f .run/*.log

web: ## Run only the dashboard in the foreground
	TEMPORAL_ADDRESS=localhost:$(TEMPORAL_PORT) KUBE_CONTEXT=$(PROFILE) go run ./cmd/web

test:
	go test ./...
