# EKS Node Monitoring Agent - Open Source Build System
# This Makefile provides a one-stop shop for building, testing, and deploying
# the EKS Node Monitoring Agent without internal dependencies.
#
# Usage:
#   make help              - Show available targets
#   make build             - Build Go code
#   make docker-build      - Build Docker image
#   make helm-lint         - Lint Helm chart
#   make deploy            - Deploy to current cluster

# =============================================================================
# Configuration Variables
# =============================================================================

# Docker registry configuration
# Set IMAGE_REGISTRY to push to a custom registry (e.g., ghcr.io/aws, your-account.dkr.ecr.region.amazonaws.com)
IMAGE_REGISTRY ?=
IMAGE_REPOSITORY ?= eks-node-monitoring-agent
IMAGE_TAG ?= latest

# Docker build arguments
GOBUILDARGS ?=

# Multi-arch build platforms
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

# Compute IMAGE_URI based on whether registry is set
ifdef IMAGE_REGISTRY
    IMAGE_URI ?= $(IMAGE_REGISTRY)/$(IMAGE_REPOSITORY):$(IMAGE_TAG)
else
    IMAGE_URI ?= $(IMAGE_REPOSITORY):$(IMAGE_TAG)
endif

# Helm chart configuration
CHART_DIR ?= charts/eks-node-monitoring-agent
CHART_OUTPUT_DIR ?= build/charts
NAMESPACE ?= kube-system

# Additional helm flags (user can override)
HELM_EXTRA_FLAGS ?=

# Helm flags for template rendering and deployment
HELM_FLAGS := --namespace $(NAMESPACE) \
              --include-crds \
              $(HELM_EXTRA_FLAGS)

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.16.5

# Go environment
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTROLLER_GEN ?= $(GOBIN)/controller-gen

# =============================================================================
# Help Target
# =============================================================================

.PHONY: help
help: ## Show this help message
	@echo "EKS Node Monitoring Agent - Available Targets"
	@echo ""
	@echo "Development:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | grep -E '(build|test|generate|fmt|vet|clean)' | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""
	@echo "Helm Operations:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | grep -E '(helm-)' | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""
	@echo "Docker Operations:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | grep -E '(docker-)' | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""
	@echo "Deployment:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | grep -E '(deploy|install-crds|render-chart)' | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""
	@echo "Configuration Variables:"
	@echo "  IMAGE_REGISTRY      Docker registry URL (default: empty, local only)"
	@echo "  IMAGE_REPOSITORY    Image repository name (default: eks-node-monitoring-agent)"
	@echo "  IMAGE_TAG           Image tag (default: latest)"
	@echo "  GOBUILDARGS         Additional Go build arguments for Docker build"
	@echo "  DOCKER_PLATFORMS    Platforms for multi-arch build (default: linux/amd64,linux/arm64)"
	@echo "  NAMESPACE           Kubernetes namespace (default: kube-system)"
	@echo "  HELM_EXTRA_FLAGS    Additional flags for helm commands"
	@echo ""
	@echo "Examples:"
	@echo "  make docker-build IMAGE_REGISTRY=your-account.dkr.ecr.us-west-2.amazonaws.com"
	@echo "  make docker-build IMAGE_REGISTRY=your-account.dkr.ecr.us-west-2.amazonaws.com IMAGE_TAG=v1.0.0"
	@echo "  make docker-build IMAGE_REGISTRY=your-account.dkr.ecr.us-west-2.amazonaws.com GOBUILDARGS='-race'"
	@echo "  make deploy HELM_EXTRA_FLAGS='--set nodeAgent.image.tag=v1.0.0'"

# =============================================================================
# Development Targets
# =============================================================================

.PHONY: build
build: generate fmt vet ## Build Go code
	go build -o bin/eks-node-monitoring-agent ./cmd/eks-node-monitoring-agent
	go build -o bin/chroot ./cmd/chroot

.PHONY: test
test: generate fmt vet covignore ## Run tests
	@echo "Running Go tests..."
	@# Only test packages that contain test files to avoid 'go: no such tool covdata'
	@# errors from coverage instrumentation on packages without tests.
	go test $$(go list ./... | while read pkg; do dir=$$(go list -f '{{.Dir}}' "$$pkg"); if ls "$$dir"/*_test.go >/dev/null 2>&1; then echo "$$pkg"; fi; done) -cover -covermode=atomic
	@echo "Running Helm chart validation..."
	@if command -v helm >/dev/null 2>&1; then \
		$(MAKE) helm-lint; \
	else \
		echo "Helm not available, skipping helm-lint"; \
	fi

.PHONY: generate
generate: mod-tidy controller-gen generate-crds generate-reasons generate-docs helm-docs ## Run all code generation

.PHONY: mod-tidy
mod-tidy: ## Tidy Go modules
	go mod tidy

.PHONY: generate-crds
generate-crds: controller-gen ## Generate CRD manifests
	$(CONTROLLER_GEN) crd object paths="./api/..." output:crd:artifacts:config=api/crds
	@# Copy CRD to Helm chart if it exists
	@if [ -d "$(CHART_DIR)/crds" ]; then \
		cp api/crds/eks.amazonaws.com_nodediagnostics.yaml $(CHART_DIR)/crds/; \
		echo "CRD copied to $(CHART_DIR)/crds/"; \
	fi

.PHONY: generate-reasons
generate-reasons: ## Generate reasons.go from YAML config
	go generate ./pkg/reasons/...

.PHONY: generate-docs
generate-docs: ## Generate AsciiDoc documentation from reasons YAML
	@mkdir -p docs
	go run ./tools/codegen-docs/... --config-path pkg/reasons/reasons.yaml > docs/node-health-issues.adoc
	@echo "Documentation generated to docs/node-health-issues.adoc"

.PHONY: fmt
fmt: ## Format Go code
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: covignore
covignore: ## Regenerate .covignore from source annotations
	hack/gen-covignore.sh > .covignore

.PHONY: clean
clean: ## Clean build artifacts
	go clean ./...
	rm -rf build/
	rm -rf bin/

.PHONY: controller-gen
controller-gen: ## Install controller-gen if needed
	@if ! command -v $(CONTROLLER_GEN) >/dev/null 2>&1 || ! $(CONTROLLER_GEN) --version | grep -q "$(CONTROLLER_GEN_VERSION)"; then \
		echo "Installing controller-gen $(CONTROLLER_GEN_VERSION)..."; \
		go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION); \
	fi

# =============================================================================
# Helm Targets
# =============================================================================

.PHONY: helm-lint
helm-lint: ## Lint Helm chart for errors
	@if command -v helm >/dev/null 2>&1; then \
		helm lint $(CHART_DIR); \
	else \
		echo "Helm not available, skipping helm-lint"; \
	fi

.PHONY: helm-template
helm-template: ## Render Helm chart templates to stdout
	@if command -v helm >/dev/null 2>&1; then \
		helm template eks-node-monitoring-agent $(CHART_DIR) $(HELM_FLAGS); \
	else \
		echo "Helm not available, skipping helm-template"; \
	fi

.PHONY: render-chart
render-chart: helm-template ## Alias for helm-template (backward compatibility)

.PHONY: helm-package
helm-package: ## Package Helm chart into .tgz archive
	@if command -v helm >/dev/null 2>&1; then \
		$(MAKE) helm-lint; \
		mkdir -p $(CHART_OUTPUT_DIR); \
		helm package $(CHART_DIR) --destination $(CHART_OUTPUT_DIR); \
		echo "Chart packaged to $(CHART_OUTPUT_DIR)/"; \
	else \
		echo "Helm not available, skipping helm-package"; \
	fi

.PHONY: helm-docs
helm-docs: ## Generate Helm chart documentation (requires Docker)
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		hack/helm-docs.sh; \
	else \
		echo "Docker not available, skipping helm-docs generation"; \
	fi

# Legacy alias for helm-docs
.PHONY: generate-helm-docs
generate-helm-docs: helm-docs

# =============================================================================
# Docker Targets
# =============================================================================

.PHONY: docker-build
docker-build: ## Build and push multi-arch Docker image (requires IMAGE_REGISTRY)
	@if [ ! -f Dockerfile ]; then \
		echo "Error: Dockerfile not found in repository root"; \
		echo "Please ensure Dockerfile exists before building"; \
		exit 1; \
	fi
	@if [ -z "$(IMAGE_REGISTRY)" ]; then \
		echo "Error: IMAGE_REGISTRY is required (multi-arch images must be pushed to a registry)"; \
		echo "Usage: make docker-build IMAGE_REGISTRY=your-registry.com"; \
		exit 1; \
	fi
	@# Handle ECR authentication if registry looks like ECR
	@if echo "$(IMAGE_REGISTRY)" | grep -q "\.ecr\."; then \
		echo "Detected ECR registry, attempting authentication..."; \
		REGION=$$(echo "$(IMAGE_REGISTRY)" | sed -n 's/.*\.ecr\.\([^.]*\)\.amazonaws\.com.*/\1/p'); \
		aws ecr get-login-password --region $$REGION | docker login --username AWS --password-stdin $(IMAGE_REGISTRY) || \
			(echo "ECR login failed. Ensure AWS credentials are configured." && exit 1); \
		aws ecr describe-repositories --repository-names $(IMAGE_REPOSITORY) --region $$REGION >/dev/null 2>&1 || \
			aws ecr create-repository --repository-name $(IMAGE_REPOSITORY) --region $$REGION; \
	fi
	@echo "Building multi-arch Docker image: $(IMAGE_URI) ($(DOCKER_PLATFORMS))"
	docker buildx build \
		--platform $(DOCKER_PLATFORMS) \
		--push \
		$(if $(GOBUILDARGS),--build-arg GOBUILDARGS="$(GOBUILDARGS)") \
		-t $(IMAGE_URI) .


.PHONY: deploy
deploy: ## Deploy to current Kubernetes cluster
	@if ! command -v kubectl >/dev/null 2>&1; then \
		echo "Error: kubectl is not installed."; \
		echo "Install kubectl: https://kubernetes.io/docs/tasks/tools/"; \
		exit 1; \
	fi
	@if ! command -v helm >/dev/null 2>&1; then \
		echo "Error: helm is not installed."; \
		echo "Install helm: https://helm.sh/docs/intro/install/"; \
		exit 1; \
	fi
	@echo "Deploying to namespace: $(NAMESPACE)"
	helm template eks-node-monitoring-agent $(CHART_DIR) $(HELM_FLAGS) | kubectl apply -f -
	@echo "Restarting daemonset to pick up changes..."
	kubectl rollout restart daemonset -n $(NAMESPACE) eks-node-monitoring-agent || true
	@echo "Deployment complete"

.PHONY: install-crds
install-crds: ## Install CRDs to current cluster
	@if ! command -v kubectl >/dev/null 2>&1; then \
		echo "Error: kubectl is not installed."; \
		echo "Install kubectl: https://kubernetes.io/docs/tasks/tools/"; \
		exit 1; \
	fi
	kubectl apply -f $(CHART_DIR)/crds/
	@echo "CRDs installed"

.PHONY: uninstall
uninstall: ## Remove deployment from current cluster
	@if ! command -v kubectl >/dev/null 2>&1; then \
		echo "Error: kubectl is not installed."; \
		exit 1; \
	fi
	helm template eks-node-monitoring-agent $(CHART_DIR) $(HELM_FLAGS) | kubectl delete -f - || true
	@echo "Deployment removed"

# =============================================================================
# E2E Test Targets
# =============================================================================

.PHONY: e2e
e2e: ## Build and run e2e tests against the current cluster context
	go test -tags=e2e -v -timeout 60m ./e2e/ --install=true --image $(IMAGE_URI) $(ARGS)

# =============================================================================
# Release Target
# =============================================================================

.PHONY: release
release: build test helm-package ## Build, test, and package for release
	@echo "Release build completed successfully"
	@echo "Artifacts:"
	@echo "  - Helm chart: $(CHART_OUTPUT_DIR)/"
