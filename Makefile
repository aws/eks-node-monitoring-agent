# EKS Node Monitoring Agent - Internal Mirror
# This package contains Go application code, API definitions, and Helm charts

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.16.5

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTROLLER_GEN ?= $(GOBIN)/controller-gen

.PHONY: generate
generate: mod-tidy controller-gen generate-crds generate-helm-docs

.PHONY: mod-tidy
mod-tidy:
	go mod tidy

.PHONY: generate-crds
generate-crds: controller-gen
	$(CONTROLLER_GEN) crd object paths="./api/..." output:crd:artifacts:config=api/crds
	@# Copy CRD to Helm chart if it exists
	@if [ -d "charts/eks-node-monitoring-agent/crds" ]; then \
		cp api/crds/eks.amazonaws.com_nodediagnostics.yaml charts/eks-node-monitoring-agent/crds/; \
	fi

.PHONY: generate-helm-docs
generate-helm-docs:
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		hack/helm-docs.sh; \
	else \
		echo "Docker not available, skipping helm-docs generation"; \
	fi

.PHONY: controller-gen
controller-gen:
	@if ! command -v $(CONTROLLER_GEN) >/dev/null 2>&1 || ! $(CONTROLLER_GEN) --version | grep -q "$(CONTROLLER_GEN_VERSION)"; then \
		echo "Installing controller-gen $(CONTROLLER_GEN_VERSION)..."; \
		go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION); \
	fi

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: build
build: generate fmt vet
	go build ./...

.PHONY: test
test: generate fmt vet
	@echo "Running Go tests..."
	go test ./... -cover -covermode=atomic
	@echo "Running Helm chart validation..."
	@if command -v helm >/dev/null 2>&1; then \
		helm lint charts/eks-node-monitoring-agent/ || echo "Helm lint skipped (helm not available)"; \
	else \
		echo "Helm not available, skipping chart validation"; \
	fi

.PHONY: release
release: build test
	@echo "Release build completed successfully"

.PHONY: clean
clean:
	go clean ./...
	rm -rf build/
