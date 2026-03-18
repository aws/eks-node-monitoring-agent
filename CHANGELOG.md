# Changelog

All notable changes to the EKS Node Monitoring Agent will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.6.1] - 2026-03-17

### What's Changed

#### Features
- Add `resizePolicy` to chart for in-place pod vertical scaling ([61eb3fb](https://github.com/aws/eks-node-monitoring-agent/commit/61eb3fb))
- Collect automode component logs in dedicated folder ([65aa2c7](https://github.com/aws/eks-node-monitoring-agent/commit/65aa2c7))

#### Bug Fixes
- Remove `helm.sh/chart` from DaemonSet selector labels to fix immutable selector upgrade failures from v1.5.x ([a7ab4ee](https://github.com/aws/eks-node-monitoring-agent/commit/a7ab4ee))
- Allowlist Calico iptables chains in UnexpectedRejectRule check to prevent false-positive warnings ([14d813e](https://github.com/aws/eks-node-monitoring-agent/commit/14d813e))

#### Documentation
- Add example for overriding ports in configuration ([ae33d75](https://github.com/aws/eks-node-monitoring-agent/commit/ae33d75))

## [v1.6.0] - 2026-03-09

### What's Changed

#### Features
- Add per-monitor configuration to selectively disable monitors ([019a715](https://github.com/aws/eks-node-monitoring-agent/commit/019a715))
- Add "node" destination for NodeDiagnostic log collection ([e4d85ac](https://github.com/aws/eks-node-monitoring-agent/commit/e4d85ac))
- Add `global.podLabels` to Helm chart ([8479714](https://github.com/aws/eks-node-monitoring-agent/commit/8479714))
- Update NodeDiagnostic CRD for node destination ([05a4038](https://github.com/aws/eks-node-monitoring-agent/commit/05a4038))

#### Bug Fixes
- Fix NodeDiagnosticController using wrong kubeclient ([611fa46](https://github.com/aws/eks-node-monitoring-agent/commit/611fa46))
- Stabilize node condition transition time for multiple errors ([3f28f13](https://github.com/aws/eks-node-monitoring-agent/commit/3f28f13))
- Ignore DCGM health code 122 (IMEX unhealthy) in soak tests ([ebfcaa5](https://github.com/aws/eks-node-monitoring-agent/commit/ebfcaa5))
- Fix e2e agent manifest to only replace agent image, preserving DCGM image ([c3fa12e](https://github.com/aws/eks-node-monitoring-agent/commit/c3fa12e))
- Make nvidia monitor e2e tests more resilient ([b2521ca](https://github.com/aws/eks-node-monitoring-agent/commit/b2521ca))
- Add `containerRegistry` override to chart for addon platform compatibility

#### CI & Build
- Merge e2e-ci into e2e test suite ([cbb92a8](https://github.com/aws/eks-node-monitoring-agent/commit/cbb92a8))
- Add Makefile support for GOBIN env var for CI/CD build systems ([4a6b5dd](https://github.com/aws/eks-node-monitoring-agent/commit/4a6b5dd))
- Include e2e test binary and charts in release target ([5cb1fbc](https://github.com/aws/eks-node-monitoring-agent/commit/5cb1fbc), [c0c9c30](https://github.com/aws/eks-node-monitoring-agent/commit/c0c9c30))
- Install helm via `go install` for build environments without helm ([fb25103](https://github.com/aws/eks-node-monitoring-agent/commit/fb25103))
- Pass instance-type override to kubetest2 in CI ([29d170f](https://github.com/aws/eks-node-monitoring-agent/commit/29d170f))
- Move accelerated hardware monitors to separate parallel e2e block ([eadfaa8](https://github.com/aws/eks-node-monitoring-agent/commit/eadfaa8))
- Reduce CI flakiness and optimize resources ([631078f](https://github.com/aws/eks-node-monitoring-agent/commit/631078f))

## [v1.5.2]

### What's Changed
- Update base DCGM image to 4.5.2-4.8.1-ubuntu22.04 to resolve CVEs ([1a2cda4](https://github.com/aws/eks-node-monitoring-agent/commit/1a2cda4))

[v1.6.1]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.0...v1.6.1
[v1.6.0]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.5.2...v1.6.0
[v1.5.2]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.5.1...v1.5.2
