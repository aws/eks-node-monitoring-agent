# kubelet/

kubelet service logs, configuration, and kubeconfig.

**Collector source:** [`pkg/log_collector/collect/kubernetes.go`](../../../pkg/log_collector/collect/kubernetes.go)

Path resolution for kubelet config and kubeconfig uses [`pkg/pathlib/kubelet.go`](../../../pkg/pathlib/kubelet.go).

---

## Files

### `kubelet.log`

kubelet service journal log for the last 10 days.

- **Command:** `journalctl -o short-iso-precise -u kubelet --since "10 days ago"` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald`, or [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on journal files in `/run/log/journal/`
- **Content:** Timestamped kubelet log lines including node registration, pod lifecycle events, volume mount operations, health check results, and errors

**Sample output (truncated):**
```
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx kubelet[1844]: I0318 22:26:10.123456    1844 server.go:440] \
  "Kubelet version" kubeletVersion="v1.32.x-eks-..."
2026-03-18T22:26:10+0000 ip-192-168-xxx-xxx kubelet[1844]: I0318 22:26:10.234567    1844 node.go:123] \
  "Setting node annotation" annotation="..." value="..."
2026-03-18T22:26:15+0000 ip-192-168-xxx-xxx kubelet[1844]: I0318 22:26:15.345678    1844 reconciler.go:224] \
  "operationExecutor.MountVolume started" ...
```

---

### `kubelet_service.txt`

The kubelet systemd unit file (resolved with drop-ins).

- **Command:** `systemctl cat kubelet` — [`systemctl(1)`](https://man7.org/linux/man-pages/man1/systemctl.1.html)
- **Linux syscall:** D-Bus socket communication with systemd — see [`sd-bus(3)`](https://man7.org/linux/man-pages/man3/sd-bus.3.html)
- **Content:** The full kubelet unit file including `[Unit]`, `[Service]`, and `[Install]` sections, showing `ExecStart` flags and environment variables
- **Not collected on:** Bottlerocket (unless EKS Auto Mode)

**Sample output (truncated):**
```ini
# /etc/systemd/system/kubelet.service
[Unit]
Description=Kubelet
After=containerd.service

[Service]
ExecStart=/usr/bin/kubelet \
  --cloud-provider external \
  --kubeconfig /etc/kubernetes/kubelet/kubeconfig \
  --config /etc/kubernetes/kubelet/config \
  --container-runtime-endpoint=unix:///run/containerd/containerd.sock \
  --hostname-override i-<instance-id> \
  --node-ip 192.168.152.126
Restart=always
```

---

### `config.json`

kubelet configuration file.

- **Source:** File copy of the kubelet config path resolved by [`pathlib.ResolveKubeletConfig()`](../../../pkg/pathlib/kubelet.go) — checks standard paths like `/etc/kubernetes/kubelet/config`, `/var/lib/kubelet/config.yaml`, etc.
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** kubelet `KubeletConfiguration` object (YAML or JSON) with settings like `clusterDNS`, `evictionHard`, `featureGates`, `maxPods`, `cgroupDriver`

**Sample output (truncated):**
```json
{
  "kind": "KubeletConfiguration",
  "apiVersion": "kubelet.config.k8s.io/v1beta1",
  "clusterDNS": ["192.168.0.10"],
  "clusterDomain": "cluster.local",
  "maxPods": 110,
  "cgroupDriver": "systemd",
  "evictionHard": {
    "memory.available": "100Mi",
    "nodefs.available": "10%"
  }
}
```

---

### `config.json.d/`

kubelet drop-in configuration directory (Kubernetes 1.29+).

- **Source:** Directory copy of the path resolved by [`pathlib.ResolveKubeletConfigDropIn()`](../../../pkg/pathlib/kubelet.go)
- **Linux syscall:** [`getdents64(2)`](https://man7.org/linux/man-pages/man2/getdents64.2.html) + [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** Individual YAML/JSON files that are merged into the base kubelet config

---

### `kubeconfig.yaml`

The kubeconfig used by kubelet to authenticate to the API server.

- **Source:** File copy of the path resolved by [`pathlib.ResolveKubeconfig()`](../../../pkg/pathlib/kubelet.go) — checks paths like `/etc/kubernetes/kubelet/kubeconfig`, `/var/lib/kubelet/kubeconfig`
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** Cluster server URL, CA certificate data (or path), and client certificate/key paths or token path

**Sample output (truncated):**
```yaml
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: <base64-ca-cert>
    server: https://XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX.gr7.eu-west-1.eks.amazonaws.com
  name: kubernetes
users:
- name: system:node:i-<instance-id>
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: /usr/bin/aws-iam-authenticator
```
