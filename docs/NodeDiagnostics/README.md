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
├── nodeadm/               # nodeadm service journal logs (hybrid nodes)
├── sandbox-image/         # pause image service log
├── storage/               # Disk, mount, inode, XFS, and pod storage info
├── sysctls/               # All kernel parameters
├── system/                # Process list, CPU/IO throttling, instance metadata, services
└── var_log/               # Selected /var/log files and kube-system pod logs
```

## Collector Index

| Subfolder | Collector Source | Platform |
|-----------|-----------------|----------|
| `bottlerocket/` | [`system.go` – `bottlerocket()`](../../pkg/log_collector/collect/system.go) | Bottlerocket only |
| `cni/` | [`cni.go`](../../pkg/log_collector/collect/cni.go) | All |
| `containerd/` | [`containerd.go`](../../pkg/log_collector/collect/containerd.go) | All |
| `ipamd/` | [`ipamd.go`](../../pkg/log_collector/collect/ipamd.go) | Non-hybrid |
| `kernel/` | [`kernel.go`](../../pkg/log_collector/collect/kernel.go) | All |
| `kubelet/` | [`kubernetes.go`](../../pkg/log_collector/collect/kubernetes.go) | All |
| `networking/` | [`networking.go`](../../pkg/log_collector/collect/networking.go), [`iptables.go`](../../pkg/log_collector/collect/iptables.go), [`nftables.go`](../../pkg/log_collector/collect/nftables.go) | All |
| `nodeadm/` | [`nodeadm.go`](../../pkg/log_collector/collect/nodeadm.go) | Hybrid nodes |
| `sandbox-image/` | [`sandbox.go`](../../pkg/log_collector/collect/sandbox.go) | AL2 |
| `storage/` | [`disk.go`](../../pkg/log_collector/collect/disk.go) | All |
| `sysctls/` | [`system.go` – `sysctl()`](../../pkg/log_collector/collect/system.go) | All |
| `system/` | [`system.go`](../../pkg/log_collector/collect/system.go), [`throttles.go`](../../pkg/log_collector/collect/throttles.go), [`instance.go`](../../pkg/log_collector/collect/instance.go), [`region.go`](../../pkg/log_collector/collect/region.go) | All |
| `var_log/` | [`commonlogs.go`](../../pkg/log_collector/collect/commonlogs.go) | All |

## Subfolder Documentation

- [bottlerocket/](./bottlerocket/README.md)
- [cni/](./cni/README.md)
- [containerd/](./containerd/README.md)
- [ipamd/](./ipamd/README.md)
- [kernel/](./kernel/README.md)
- [kubelet/](./kubelet/README.md)
- [networking/](./networking/README.md)
- [nodeadm/](./nodeadm/README.md)
- [sandbox-image/](./sandbox-image/README.md)
- [storage/](./storage/README.md)
- [sysctls/](./sysctls/README.md)
- [system/](./system/README.md)
- [var_log/](./var_log/README.md)

## Platform Tags

Collectors use tags to conditionally skip or include collection steps:

| Tag | Meaning |
|-----|---------|
| `bottlerocket` | Node runs Bottlerocket OS |
| `nvidia` | Node has NVIDIA GPU |
| `eks-auto` | Node is an EKS Auto Mode node |
| `eks-hybrid` | Node is an EKS Hybrid node |

Tags are defined in [`pkg/log_collector/collect/accessor.go`](../../pkg/log_collector/collect/accessor.go).
