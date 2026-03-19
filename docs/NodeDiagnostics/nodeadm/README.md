# nodeadm/

Journal logs for `nodeadm` systemd services used on EKS Hybrid nodes.

**Collector source:** [`pkg/log_collector/collect/nodeadm.go`](../../../pkg/log_collector/collect/nodeadm.go)

`nodeadm` is the node initialization tool for EKS Hybrid nodes. It runs as two systemd services: one for configuration and one for the main runtime loop.

---

## Files

### `nodeadm-config.log`

Journal log for the `nodeadm-config` systemd service.

- **Command:** `journalctl -o short-iso-precise -u nodeadm-config`
- **Linux syscall:** `AF_UNIX` socket to `systemd-journald`, or `open(2)` on journal files
- **Content:** Log output from the `nodeadm-config` service, which handles initial node configuration: writing kubelet config, setting up credentials, and preparing the node for joining the cluster

**Sample output:**
```
2026-03-18T22:26:05+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"Starting nodeadm config"}
2026-03-18T22:26:05+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"Writing kubelet config"}
2026-03-18T22:26:06+0000 ip-192-168-xxx-xxx nodeadm-config[1234]: {"level":"info","ts":"...","msg":"nodeadm config complete"}
```

---

### `nodeadm-run.log`

Journal log for the `nodeadm-run` systemd service.

- **Command:** `journalctl -o short-iso-precise -u nodeadm-run`
- **Linux syscall:** `AF_UNIX` socket to `systemd-journald`, or `open(2)` on journal files
- **Content:** Log output from the `nodeadm-run` service, which manages the ongoing node lifecycle: credential refresh, health monitoring, and node deregistration

**Sample output:**
```
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx nodeadm-run[1456]: {"level":"info","ts":"...","msg":"Starting nodeadm run"}
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx nodeadm-run[1456]: {"level":"info","ts":"...","msg":"Node registered successfully"}
```
