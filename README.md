# EKS Node Monitoring Agent

The EKS Node Monitoring Agent detects health issues on Amazon EKS worker nodes by parsing system logs and surfacing status information through Kubernetes `NodeConditions`. When paired with Amazon EKS node auto repair, detected issues can trigger automatic node replacement or reboot.

For detailed configuration options and usage documentation, refer to the [Amazon EKS Node Health documentation](https://docs.aws.amazon.com/eks/latest/userguide/node-health.html).

## Overview

The agent runs as a DaemonSet on each node and monitors for issues across several categories:

- **Kernel** - Process limits, kernel bugs, soft lockups
- **Networking** - VPC CNI (IPAMD) issues, interface problems, connectivity
- **Storage** - EBS throughput/IOPS limits, I/O delays
- **Container Runtime** - Pod termination issues, probe failures
- **Accelerated Hardware** - NVIDIA GPU errors (XID codes), AWS Neuron issues, DCGM diagnostics

For each category, the agent applies a dedicated `NodeCondition` to worker nodes (e.g., `KernelReady`, `NetworkingReady`, `StorageReady`, `AcceleratedHardwareReady`). These conditions integrate with Amazon EKS node auto repair to automatically remediate unhealthy nodes.

## Project Layout

```
.
├── api/                    # API definitions and CRDs
├── charts/                 # Helm chart for deployment
├── cmd/                    # Application entry point
├── examples/               # Integration examples
├── hack/                   # Build and utility scripts
├── monitors/               # Health monitoring plugins
├── pkg/                    # Core packages
└── test/                   # Integration tests
```

## Installation

It is recommended to install the EKS Node Health Monitoring Agent as an EKS add-on. For Helm installation instructions, see [charts/eks-node-monitoring-agent/README.md](./charts/eks-node-monitoring-agent/README.md).

For detailed configuration options and usage documentation, refer to the [Amazon EKS Node Health documentation](https://docs.aws.amazon.com/eks/latest/userguide/node-health.html).

## Configuring Monitors

By default all monitors are enabled. Individual monitors can be disabled via the Helm chart's `nodeAgent.monitors` configuration or by providing a config file at `/etc/nma/config.yaml`.

### Helm Values

Each monitor supports `enabled: true/false` to enable or disable it:

```yaml
nodeAgent:
  monitors:
    networking:
      enabled: false
    neuron:
      enabled: false
```

The networking monitor additionally supports `allowedIPTablesChains` to suppress `UnexpectedRejectRule` warnings for rules in custom chains. Entries must use `table/chain` format:

```yaml
nodeAgent:
  monitors:
    networking:
      allowedIPTablesChains:
        - "filter/MY-CUSTOM-CHAIN"
```

The networking monitor also supports `excludedInterfaceNameRegexps` to suppress `InterfaceNotUp` / `InterfaceNotRunning` findings for interfaces that are not part of Kubernetes node networking. This is useful on accelerated instance types (e.g. P6) that expose host-visible Mellanox/NVIDIA IPoIB interfaces such as `ibp115s0f0`, which may legitimately remain down. Each entry is a Go regular expression matched against the interface name; invalid regexps fail fast at startup.

By default the following interfaces are excluded: kernel tunnel fallback devices (`gre*`, `gretap*`, `erspan*`, `ip6gre*`, `ip6gretap*`, `tunl*`, `sit*`, `ip6tnl*`) and InfiniBand / IPoIB interfaces (`ib*`, `ibp*`). Setting `excludedInterfaceNameRegexps` overrides this default; set it to an empty list to disable exclusions entirely.

```yaml
nodeAgent:
  monitors:
    networking:
      excludedInterfaceNameRegexps:
        - "^ibp[0-9]+s[0-9]+f[0-9]+$"
        - "^ib[0-9]+$"
```

### Disabling individual reasons

Every monitor supports `disabledReasons` to suppress specific findings without turning off the monitor as a whole. Any condition with a matching reason is dropped before it is exported to the node status, events or metrics. Reason names can be found in `docs/node-health-issues.adoc`; parameterized reasons are specified in their rendered form (e.g. `NvidiaXID79Error`). Entries are not validated against a fixed catalog, so a name that does not match an emitted reason has no effect.

For example, to suppress `IPAMDNotReady` findings:

```yaml
nodeAgent:
  monitors:
    networking:
      disabledReasons:
        - "IPAMDNotReady"
```

### Config File Format

The agent reads a YAML config file mounted at `/etc/nma/config.yaml`. Omitted monitors default to enabled.

```yaml
monitors:
  kernel-monitor:
    enabled: true
  networking:
    enabled: false
  storage-monitor:
    enabled: true
  nvidia:
    enabled: true
  neuron:
    enabled: false
  runtime:
    enabled: true
```

Valid plugin names: `kernel-monitor`, `networking`, `storage-monitor`, `nvidia`, `neuron`, `runtime`.

When a monitor is disabled:

- Its health checks are not executed.
- The corresponding `NodeCondition` (e.g., `NetworkingReady`) is not set on the node, avoiding false-positive healthy status for unmonitored subsystems.

When a reason is disabled via `disabledReasons`:

- The monitor keeps running and its other checks remain active.
- Conditions with the disabled reason are never exported, so they do not affect the node status or trigger auto-repair actions.

## Building

```bash
# Build the binary
make build

# Run tests
make test

# Build container image
make docker-build
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on:

- Reporting bugs and feature requests
- Submitting pull requests
- Code of conduct
- Security issue notifications

## Security

If you discover a potential security issue, please report it via the [AWS vulnerability reporting page](http://aws.amazon.com/security/vulnerability-reporting/). Do not create a public GitHub issue for security vulnerabilities.

See [CONTRIBUTING.md](CONTRIBUTING.md#security-issue-notifications) for more information.

## License

This project is licensed under the Apache-2.0 License. See [LICENSE](LICENSE) for the full license text.
