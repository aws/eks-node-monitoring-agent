# NodeDiagnostics Log Collection

The `NodeDiagnostics` resource triggers on-demand log collection from an EKS worker node. When a `NodeDiagnostic` object is created (typically by a system administrator or SRE), the agent collects a structured snapshot of the node's state and stores it as a compressed archive.

The controller that handles this resource lives in [`pkg/controllers/nodediagnostic.go`](../../pkg/controllers/nodediagnostic.go). The collection framework is in [`pkg/log_collector/`](../../pkg/log_collector/) and individual collectors are in [`pkg/log_collector/collect/`](../../pkg/log_collector/collect/).

## Output Structure

```
<collection-root>/
├── bottlerocket/          # Bottlerocket-specific inventory and logdog output
├── cni/                   # CNI plugin configuration
├── containerd/            # containerd runtime state and logs
├── ipamd/                 # VPC CNI IPAMD introspection data
├── kernel/                # Kernel ring buffer and version
├── kubelet/               # kubelet logs, config, and kubeconfig
├── networking/            # IP rules, routes, iptables, nftables, conntrack, ethtool
├── nodeadm/               # nodeadm service journal logs (AL2023 and hybrid nodes)
├── sandbox-image/         # pause image service log
├── storage/               # Disk, mount, inode, XFS, and pod storage info
├── sysctls/               # All kernel parameters
├── system/                # Process list, CPU/IO throttling, instance metadata, services
└── var_log/               # Selected /var/log files and kube-system pod logs
```

## Collector Index

| Subfolder | Collector Source | Platform |
|-----------|-----------------|----------|
| [`bottlerocket/`](./bottlerocket/README.md) | [`system.go` – `bottlerocket()`](../../pkg/log_collector/collect/system.go) | Bottlerocket only |
| [`cni/`](./cni/README.md) | [`cni.go`](../../pkg/log_collector/collect/cni.go) | All |
| [`containerd/`](./containerd/README.md) | [`containerd.go`](../../pkg/log_collector/collect/containerd.go) | All |
| [`ipamd/`](./ipamd/README.md) | [`ipamd.go`](../../pkg/log_collector/collect/ipamd.go) | Non-hybrid |
| [`kernel/`](./kernel/README.md) | [`kernel.go`](../../pkg/log_collector/collect/kernel.go) | All |
| [`kubelet/`](./kubelet/README.md) | [`kubernetes.go`](../../pkg/log_collector/collect/kubernetes.go) | All |
| [`networking/`](./networking/README.md) | [`networking.go`](../../pkg/log_collector/collect/networking.go), [`iptables.go`](../../pkg/log_collector/collect/iptables.go), [`nftables.go`](../../pkg/log_collector/collect/nftables.go) | All |
| [`nodeadm/`](./nodeadm/README.md) | [`nodeadm.go`](../../pkg/log_collector/collect/nodeadm.go) | AL2023 and Hybrid nodes |
| [`sandbox-image/`](./sandbox-image/README.md) | [`sandbox.go`](../../pkg/log_collector/collect/sandbox.go) | AL2 |
| [`storage/`](./storage/README.md) | [`disk.go`](../../pkg/log_collector/collect/disk.go) | All |
| [`sysctls/`](./sysctls/README.md) | [`system.go` – `sysctl()`](../../pkg/log_collector/collect/system.go) | All |
| [`system/`](./system/README.md) | [`system.go`](../../pkg/log_collector/collect/system.go), [`throttles.go`](../../pkg/log_collector/collect/throttles.go), [`instance.go`](../../pkg/log_collector/collect/instance.go), [`region.go`](../../pkg/log_collector/collect/region.go) | All |
| [`var_log/`](./var_log/README.md) | [`commonlogs.go`](../../pkg/log_collector/collect/commonlogs.go) | All |

## Platform Tags

Collectors use tags to conditionally skip or include collection steps:

| Tag | Meaning |
|-----|---------|
| `bottlerocket` | Node runs [Bottlerocket](https://github.com/bottlerocket-os/bottlerocket) OS |
| `nvidia` | Node has NVIDIA GPU |
| `eks-auto` | Node is an [EKS Auto Mode](https://docs.aws.amazon.com/eks/latest/userguide/automode.html) node |
| `eks-hybrid` | Node is an [EKS Hybrid](https://docs.aws.amazon.com/eks/latest/userguide/hybrid-nodes-overview.html) node |

Tags are defined in [`pkg/log_collector/collect/accessor.go`](../../pkg/log_collector/collect/accessor.go).

EKS optimized OS images for AL2 and AL2023 are built from the [Amazon EKS AMI Build Specification](https://github.com/awslabs/amazon-eks-ami/tree/main).
