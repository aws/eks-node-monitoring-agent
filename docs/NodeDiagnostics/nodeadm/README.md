# nodeadm/

Journal logs for `nodeadm` systemd services used on AL2023 and EKS Hybrid nodes.

**Collector source:** [`pkg/log_collector/collect/nodeadm.go`](../../../pkg/log_collector/collect/nodeadm.go)

`nodeadm` is the node initialization tool used on **AL2023** ([Amazon Linux 2023](https://github.com/amazonlinux/amazon-linux-2023) managed EC2 nodes) and **EKS Hybrid** nodes. It replaces the older `bootstrap.sh` script (from the [Amazon EKS AMI Build Specification](https://github.com/awslabs/amazon-eks-ami/tree/main)) and is documented at [awslabs/amazon-eks-ami — nodeadm](https://github.com/awslabs/amazon-eks-ami/blob/main/nodeadm/README.md). It runs as two systemd services: one for configuration and one for the main runtime loop. On nodes where these services are not present (e.g. [Bottlerocket](https://github.com/bottlerocket-os/bottlerocket), AL2), the log files will contain `-- No entries --`.

---

## Files

### `nodeadm-config.log`

Journal log for the `nodeadm-config` systemd service.

- **Command:** `journalctl -o short-iso-precise -u nodeadm-config` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald`, or [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on journal files
- **Content:** Log output from the `nodeadm-config` service, which handles initial node configuration: writing kubelet config, setting up credentials, and preparing the node for joining the cluster. On AL2023 this runs once at boot to configure the node; on Hybrid nodes it also handles on-premises credential setup (e.g. AWS SSM or IAM Roles Anywhere)

**Sample output:**
```
2026-03-18T22:26:05+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"Starting nodeadm config"}
2026-03-18T22:26:05+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"Writing kubelet config"}
2026-03-18T22:26:06+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"nodeadm config complete"}
```

---

### `nodeadm-run.log`

Journal log for the `nodeadm-run` systemd service.

- **Command:** `journalctl -o short-iso-precise -u nodeadm-run` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald`, or [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on journal files
- **Content:** Log output from the `nodeadm-run` service, which manages the ongoing node lifecycle: credential refresh, health monitoring, and node deregistration. On Hybrid nodes this service runs continuously to refresh on-premises credentials; on AL2023 EC2 nodes it is typically a no-op after initial setup

**Sample output:**
```
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx nodeadm-run[1456]: {"level":"info","ts":"...","msg":"Starting nodeadm run"}
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx nodeadm-run[1456]: {"level":"info","ts":"...","msg":"Node registered successfully"}
```
