# sandbox-image/

Journal log for the `sandbox-image` systemd service.

**Collector source:** [`pkg/log_collector/collect/sandbox.go`](../../../pkg/log_collector/collect/sandbox.go)

The `sandbox-image` service pre-pulls the pause container image used as the pod sandbox (infrastructure container). It runs on Amazon Linux 2 nodes.

**Note:** This collector is applicable to AL2 nodes. It is expected to be removed after AL2 reaches end-of-life.

---

## Files

### `sandbox-image-log.txt`

Journal log for the `sandbox-image` systemd service.

- **Command:** `journalctl -o short-iso-precise -u sandbox-image` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald`, or [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on journal files in `/run/log/journal/`
- **Content:** Log output from the service that pulls and caches the pause image before kubelet starts, ensuring the sandbox image is available locally

**Sample output:**
```
2026-03-18T22:26:05+0000 ip-192-168-xxx-xxx sandbox-image[1234]: Pulling sandbox image: 602401143452.dkr.ecr.eu-west-1.amazonaws.com/eks/pause:3.10-eksbuild.1
2026-03-18T22:26:06+0000 ip-192-168-xxx-xxx sandbox-image[1234]: Successfully pulled sandbox image
```

If this service fails, kubelet may be unable to start pods because the pause container image is unavailable.
