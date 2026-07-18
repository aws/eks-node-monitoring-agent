# var_log/

Selected files from `/var/log/` and pod logs from `/var/log/pods/` for key kube-system components.

**Collector source:** [`pkg/log_collector/collect/commonlogs.go`](../../../pkg/log_collector/collect/commonlogs.go)

---

## Structure

```
var_log/
├── aws-routed-eni/          # VPC CNI plugin log files
│   ├── ipamd.log
│   ├── plugin.log
│   ├── ebpf-sdk.log
│   ├── egress-v6-plugin.log
│   └── network-policy-agent.log
├── syslog                   # System log (Ubuntu/Debian)
├── messages                 # System log (RHEL/Amazon Linux)
├── cloud-init.log           # cloud-init execution log
├── cloud-init-output.log    # cloud-init stdout/stderr
├── kube-proxy.log           # kube-proxy log (if file-based)
└── kube-system_<pod>_<uid>/ # Pod logs for kube-system components
    └── <container>/
        └── 0.log
```

---

## `/var/log/` Files

The following files are copied directly from `/var/log/` if they exist:

| File | Content |
|------|---------|
| `syslog` | System messages (Ubuntu/Debian) |
| `messages` | System messages (RHEL/Amazon Linux) |
| `aws-routed-eni/` | VPC CNI plugin logs (directory) |
| `cron` | Cron job execution log |
| `cloud-init.log` | cloud-init module execution |
| `cloud-init-output.log` | cloud-init command stdout/stderr |
| `user-data.log` | EC2 user data script output |
| `kube-proxy.log` | kube-proxy log (file-based logging) |

- **Linux syscall:** [`stat(2)`](https://man7.org/linux/man-pages/man2/stat.2.html) to check existence; [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) for files; [`getdents64(2)`](https://man7.org/linux/man-pages/man2/getdents64.2.html) for directories

---

## `aws-routed-eni/` — VPC CNI Logs

### `ipamd.log`

IPAMD daemon log written directly to file by the `aws-node` pod.

- **Content:** IP allocation events, ENI attachment/detachment, EC2 API calls, and errors

**Sample output (truncated):**
```
2026-03-18T22:26:15Z [INFO] ipamd/ipamd.go:xxx Starting IPAMD
2026-03-18T22:26:15Z [INFO] ipamd/ipamd.go:xxx Attaching ENI eni-<eni-id>
2026-03-18T22:26:16Z [INFO] ipamd/ipamd.go:xxx Successfully assigned IP 192.168.152.64 to pod kube-system/coredns-xxx
```

### `plugin.log`

CNI plugin log written during pod network setup/teardown.

- **Content:** Per-invocation log of the `aws-cni` binary: ADD/DEL operations, IP assignment, veth pair creation

**Sample output (truncated):**
```
2026-03-18T22:26:20Z [DEBUG] plugin/plugin.go:xxx ADD called for pod kube-system/coredns-xxx
2026-03-18T22:26:20Z [DEBUG] plugin/plugin.go:xxx Assigned IP 192.168.152.64 to pod
2026-03-18T22:26:20Z [DEBUG] plugin/plugin.go:xxx Created veth pair eni1d44c52eb2c <-> eth0
```

### `ebpf-sdk.log`

eBPF SDK log for network policy enforcement.

- **Content:** eBPF program load/unload events and policy enforcement decisions

### `egress-v6-plugin.log`

IPv6 egress plugin log.

- **Content:** IPv6 NAT64/masquerade operations for pods with IPv4-only connectivity

### `network-policy-agent.log`

AWS Network Policy Agent log.

- **Content:** NetworkPolicy reconciliation, eBPF map updates, and policy enforcement events

---

## Pod Logs

Pod logs are collected for the following kube-system pods (matched by glob pattern):

| Pattern | Component |
|---------|-----------|
| `kube-system_aws-node*` | VPC CNI (aws-node) |
| `kube-system_cni-metrics-helper*` | CNI metrics helper |
| `kube-system_coredns*` | CoreDNS |
| `kube-system_kube-proxy*` | kube-proxy |
| `kube-system_ebs-csi-*` | EBS CSI driver |
| `kube-system_efs-csi-*` | EFS CSI driver |
| `kube-system_fsx-csi-*` | FSx CSI driver |
| `kube-system_eks-pod-identity-agent*` | EKS Pod Identity Agent |

Pod logs are stored under `/var/log/pods/<namespace>_<pod-name>_<uid>/<container>/0.log`.

- **Linux syscall:** [`glob(3)`](https://man7.org/linux/man-pages/man3/glob.3.html) pattern matching on `/var/log/pods/`; [`getdents64(2)`](https://man7.org/linux/man-pages/man2/getdents64.2.html) + [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) for recursive copy

**Sample directory structure:**
```
var_log/
└── kube-system_coredns-fd7d56586-xxxxx_00aae29f-98c1-4e52-a24f-4a32cd5da5da/
    └── coredns/
        └── 0.log
```

**Sample pod log content:**
```
2026-03-18T22:26:20.123456789Z stdout F [INFO] plugin/reload.go:xxx Reloading
2026-03-18T22:26:20.234567890Z stdout F .:53
2026-03-18T22:26:20.345678901Z stdout F [INFO] CoreDNS-1.11.x
```

Log format: `<timestamp> <stream> <flags> <message>` where stream is `stdout` or `stderr` and flags are `F` (full line) or `P` (partial).
