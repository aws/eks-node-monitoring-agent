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

noop!