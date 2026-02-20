.PHONY: generate
generate:
	hack/helm-docs.sh

# E2E test targets
.PHONY: update-e2e-manifests
update-e2e-manifests:
	@echo "Generating e2e agent manifest template..."
	@helm template eks-node-monitoring-agent charts/eks-node-monitoring-agent \
		--namespace kube-system \
		--include-crds | \
		sed 's|image: [^[:space:]]*|image: {{ .Image }}|g' > e2e-ci/setup/manifests/agent.tpl.yaml
	@echo "Generated e2e-ci/setup/manifests/agent.tpl.yaml"

.PHONY: build-e2e
build-e2e:
	@echo "Building e2e test binary..."
	@mkdir -p bin
	$(MAKE) -C e2e-ci build
	@echo "Built bin/e2e.test"

.PHONY: e2e
e2e: update-e2e-manifests build-e2e
	@echo "Running e2e tests..."
	./bin/e2e.test --test.v --test.timeout 60m --install=true --image=$(IMAGE)

