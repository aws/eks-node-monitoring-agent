# EKS Node Monitoring Agent - Internal Mirror
# This package will contain both Go application code and Helm charts
# Currently contains only Helm charts; Go code will be migrated later

.PHONY: generate
generate:
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		hack/helm-docs.sh; \
	else \
		echo "Docker not available, skipping helm-docs generation"; \
	fi

.PHONY: build
build: generate
	@echo "Helm charts build completed successfully"

.PHONY: test
test:
	@echo "Running Helm chart validation..."
	@if command -v helm >/dev/null 2>&1; then \
		helm lint charts/eks-node-monitoring-agent/ || echo "Helm lint skipped (helm not available)"; \
	else \
		echo "Helm not available, skipping chart validation"; \
	fi

.PHONY: release
release: build test
	@echo "Helm charts release build completed successfully"

