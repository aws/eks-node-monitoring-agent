# Changelog

All notable changes to the EKS Node Monitoring Agent will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.6.5] - 2026-05-14

### What's Changed

#### Features
- Add fabric manager detection for NVIDIA GPUs ([d6a3279](https://github.com/aws/eks-node-monitoring-agent/commit/d6a3279d8ad860cdc7856b68f972f97a6e1456ae))
- Add well-known XID codes from ECS agent and document resolution buckets ([47ac6d4](https://github.com/aws/eks-node-monitoring-agent/commit/47ac6d49148fb198cc6eb92adf05ba251b0abe19))

#### Bug Fixes
- Allowlist kube-proxy IPVS-mode reject chains ([b266023](https://github.com/aws/eks-node-monitoring-agent/commit/b2660236fcb79c064ec4a720e5708092696a82a1))
- Fix race condition in NodeDiagnostic CRD controller ([84e2371](https://github.com/aws/eks-node-monitoring-agent/commit/84e23718105f8062862547d788f610d2dda2362d))

#### Dependencies
- Bump `github.com/moby/spdystream` from 0.5.0 to 0.5.1 ([81d2a72](https://github.com/aws/eks-node-monitoring-agent/commit/81d2a72bf0e85cb09f039cc1494bc3a637b139e5))

#### CI & Build
- Bump Dockerfile Go version to 1.26.2 to fix CI ([fc51d2e](https://github.com/aws/eks-node-monitoring-agent/commit/fc51d2ebd4056a3ece080fe5d407394fe7162faf))
- Scope down bot permissions and disable caching in GitHub Actions ([42ee911](https://github.com/aws/eks-node-monitoring-agent/commit/42ee91140ad5b94dc90017229187d654d71466be))
- Refine NVIDIA e2e test execution ([ce19306](https://github.com/aws/eks-node-monitoring-agent/commit/ce19306df2a708a147719b811dbd230836f0a54c))
- Bump wait time for instance termination in e2e tests ([682a60f](https://github.com/aws/eks-node-monitoring-agent/commit/682a60f11608a566bb600f5aff334e022be694a4))
- Add CODEOWNERS file ([f9a071c](https://github.com/aws/eks-node-monitoring-agent/commit/f9a071c189383fd46dd292bcc43b430401a2c513))

## [v1.6.4] - 2026-04-10

### What's Changed

#### Features
- Allow users to allowlist custom iptables rules ([601a439](https://github.com/aws/eks-node-monitoring-agent/commit/601a4390664aa526d736d6d67aa4efde9662b996))
- Update well known XID codes ([4e89770](https://github.com/aws/eks-node-monitoring-agent/commit/4e89770c5cb345e16e7a2515c808bd2617d721bb))

#### Bug Fixes
- Bubble up S3 Upload errors to ND failure message ([1eeb36f](https://github.com/aws/eks-node-monitoring-agent/commit/1eeb36ffd35b26cf6add81730b871b59a83e8da9))
- Enforce VPC CNI pod name as a prefix ([165bf5e](https://github.com/aws/eks-node-monitoring-agent/commit/165bf5e83b70be59e9c9e1042496d1b471123b09))
- Ensure VPC CNI pod by init container ([993719d](https://github.com/aws/eks-node-monitoring-agent/commit/993719df643837ce650d0e47ea24d4ee5b341a95))

#### Dependencies
- Bump up the go version to 1.26.2 ([d0cb78a](https://github.com/aws/eks-node-monitoring-agent/commit/d0cb78a2115155d1fe52fe1cd6408d5e4bd48fe8))
- Update Go dependencies ([4cd1272](https://github.com/aws/eks-node-monitoring-agent/commit/4cd127250695f31a47fa09fd2d392ec003e914c9))

#### CI & Build
- Add CI to auto-update GPU list ([e85832c](https://github.com/aws/eks-node-monitoring-agent/commit/e85832c0e3796e7d7549e8d41c725a7b291b67f9))
- Add minimal permissions block to restrict GITHUB_TOKEN to read-only access ([e70f80e](https://github.com/aws/eks-node-monitoring-agent/commit/e70f80e5436c4f4c5069444f14b6cc5726bea4b2))
- CI runs only in parent repo ([11bacd6](https://github.com/aws/eks-node-monitoring-agent/commit/11bacd64a0b958dba289e2ad33dc409ba1104b7a))

## [v1.6.3] - 2026-04-03

### What's Changed

#### Features
- Add tcpdump packet capture support ([aea0cec](https://github.com/aws/eks-node-monitoring-agent/commit/aea0cec))
- Reduce noisy logs on clusters with alternative CNIs ([a3d468b](https://github.com/aws/eks-node-monitoring-agent/commit/a3d468b))
- Upgrade containerd from 1.7.8 to 2.2.1 ([d04fc2c](https://github.com/aws/eks-node-monitoring-agent/commit/d04fc2c))
- Make probe and affinities configurable ([c54c7c8](https://github.com/aws/eks-node-monitoring-agent/commit/c54c7c8))

#### Bug Fixes
- Tolerate IPAMD pod teardown ([45f85de](https://github.com/aws/eks-node-monitoring-agent/commit/45f85de))
- Short circuit in IPAMD proc lookup ([48e563c](https://github.com/aws/eks-node-monitoring-agent/commit/48e563c))
- Tolerate IPAMD startup up to ipamd monitor interval ([93c547f](https://github.com/aws/eks-node-monitoring-agent/commit/93c547f))
- Fix inconsistency between probe ports args in helm charts and addon configuration ([26869aa](https://github.com/aws/eks-node-monitoring-agent/commit/26869aa))

#### Dependencies
- Bump Go version to 1.26 ([78411a1](https://github.com/aws/eks-node-monitoring-agent/commit/78411a1))
- Update Go dependencies ([dfee87e](https://github.com/aws/eks-node-monitoring-agent/commit/dfee87e))

#### CI & Build
- Add CI to update Go deps ([9a4d17e](https://github.com/aws/eks-node-monitoring-agent/commit/9a4d17e))
- Automatically bump dcgm-exporter image version ([bed7e05](https://github.com/aws/eks-node-monitoring-agent/commit/bed7e05))
- Add kubetest2 sweeper to handle clean up of stale leaked resources ([025c96c](https://github.com/aws/eks-node-monitoring-agent/commit/025c96c))
- Optimize CI actions for e2e testing ([cdb2482](https://github.com/aws/eks-node-monitoring-agent/commit/cdb2482))
- Run unit test on PR creation ([cbbf06e](https://github.com/aws/eks-node-monitoring-agent/commit/cbbf06e))

## [v1.6.2] - 2026-03-23

### What's Changed

#### Features
- Add `kubectl ekslogs` plugin for NodeDiagnostic log collection ([a2a9660](https://github.com/aws/eks-node-monitoring-agent/commit/a2a9660))
- Add ZRAM usage monitoring to kernel monitor ([b7d3ed3](https://github.com/aws/eks-node-monitoring-agent/commit/b7d3ed3))
- Change `NvidiaDeviceCountMismatch` severity from Warning to Fatal ([8379e15](https://github.com/aws/eks-node-monitoring-agent/commit/8379e15))
- Add g7e instances to NVIDIA DCGM affinity list ([c46738a](https://github.com/aws/eks-node-monitoring-agent/commit/c46738a))

#### Bug Fixes
- Add `-o short-iso-precise` to all journalctl invocations for consistent ISO 8601 timestamps with timezone offset ([442308a](https://github.com/aws/eks-node-monitoring-agent/commit/442308a))

#### Dependencies
- Bump `google.golang.org/grpc` from 1.79.2 to 1.79.3 ([1fe8681](https://github.com/aws/eks-node-monitoring-agent/commit/1fe8681))
- Update Go dependencies ([929dce6](https://github.com/aws/eks-node-monitoring-agent/commit/929dce6))

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

[v1.6.5]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.4...v1.6.5
[v1.6.4]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.3...v1.6.4
[v1.6.3]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.2...v1.6.3
[v1.6.2]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.1...v1.6.2
[v1.6.1]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.6.0...v1.6.1
[v1.6.0]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.5.2...v1.6.0
[v1.5.2]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.5.1...v1.5.2
