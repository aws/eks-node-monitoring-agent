# containerd/

containerd container runtime state, configuration, and journal logs.

**Collector source:** [`pkg/log_collector/collect/containerd.go`](../../../pkg/log_collector/collect/containerd.go)

---

## Files

### `containerd-config.txt`

The effective (merged) containerd configuration.

- **Command:** `containerd config dump` — [`containerd(8)`](https://github.com/containerd/containerd/blob/main/docs/man/containerd.8.md)
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) on the containerd gRPC socket (`/run/containerd/containerd.sock`), or subprocess exec
- **Content:** Full TOML configuration including snapshotter, runtime, and plugin settings

**Sample output (truncated):**
```toml
version = 3
[grpc]
  address = "/run/containerd/containerd.sock"
[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "overlayfs"
      default_runtime_name = "runc"
```

---

### `containerd-log.txt`

containerd service journal log.

- **Command:** `journalctl -o short-iso-precise -u containerd` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald` or [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on journal files in `/run/log/journal/`
- **Content:** Timestamped containerd log lines including container start/stop events, snapshot operations, and errors

**Sample output (truncated):**
```
2026-03-18T22:26:07+0000 ip-192-168-xxx-xxx containerd[1730]: time="2026-03-18T22:26:07.123Z" \
  level=info msg="starting containerd" revision="..." version="v1.7.x"
2026-03-18T22:26:07+0000 ip-192-168-xxx-xxx containerd[1730]: time="..." \
  level=info msg="loading plugin" type=io.containerd.snapshotter.v1 id=overlayfs
```

---

### `containerd-version.txt`

containerd and `ctr` client version information.

- **Command:** `ctr version` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) on `/run/containerd/containerd.sock` (gRPC)
- **Content:** Client and server version, revision, and Go version

**Sample output:**
```
Client:
  Version:  v1.7.25
  Revision: ...
  Go version: go1.22.x

Server:
  Version:  v1.7.25
  Revision: ...
  UUID: ...
```

---

### `containerd-namespaces.txt`

List of containerd namespaces.

- **Command:** `ctr namespaces list` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** gRPC over [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) on containerd socket
- **Content:** All namespaces; Kubernetes workloads use the `k8s.io` namespace

**Sample output:**
```
NAME    LABELS
k8s.io
```

---

### `containerd-images.txt`

Container images in the `k8s.io` namespace.

- **Command:** `ctr --namespace k8s.io images list` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** gRPC over containerd socket
- **Content:** Image reference, digest, media type, size, and labels

**Sample output (truncated):**
```
REF                                                                    TYPE                                                 DIGEST                  SIZE      PLATFORMS   LABELS
602401143452.dkr.ecr.eu-west-1.amazonaws.com/eks/pause:3.10-eksbuild.1  application/vnd.oci.image.manifest.v1+json  sha256:...  683.5 KiB  linux/arm64  -
public.ecr.aws/eks-distro/kubernetes/pause:3.10-eks-1-32-latest          application/vnd.oci.image.manifest.v1+json  sha256:...  683.5 KiB  linux/arm64  -
```

---

### `containerd-containers.txt`

Containers in the `k8s.io` namespace.

- **Command:** `ctr --namespace k8s.io containers list` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** gRPC over containerd socket
- **Content:** Container ID, image, and runtime

**Sample output (truncated):**
```
CONTAINER                                                           IMAGE                                                    RUNTIME
06b8caac3eb5fb3cbe5aeafe18fc0d3e16367d3ddd4a0b607e42505e6716b0e6  ...pause:3.10-eksbuild.1                                 io.containerd.runc.v2
22e691e501fe8ee5bf8c7c35f9b29f38de086d4046dfb568161927fb39580bc5  ...amazon-cloudwatch-agent:...                            io.containerd.runc.v2
```

---

### `containerd-tasks.txt`

Running tasks (container processes) in the `k8s.io` namespace.

- **Command:** `ctr --namespace k8s.io tasks list` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** gRPC over containerd socket
- **Content:** Task ID, PID, and status (RUNNING, STOPPED, etc.)

**Sample output (truncated):**
```
TASK                                                                PID     STATUS
06b8caac3eb5fb3cbe5aeafe18fc0d3e16367d3ddd4a0b607e42505e6716b0e6  2184    RUNNING
22e691e501fe8ee5bf8c7c35f9b29f38de086d4046dfb568161927fb39580bc5  2375    RUNNING
```

---

### `containerd-plugins.txt`

Loaded containerd plugins and their status.

- **Command:** `ctr --namespace k8s.io plugins list` — [`ctr(8)`](https://github.com/containerd/containerd/blob/main/docs/man/ctr.8.md)
- **Linux syscall:** gRPC over containerd socket
- **Content:** Plugin type, ID, platforms, and status (ok / error)

**Sample output (truncated):**
```
TYPE                                   ID                    PLATFORMS   STATUS
io.containerd.content.v1               content               -           ok
io.containerd.snapshotter.v1           overlayfs             linux/arm64 ok
io.containerd.runtime.v2               io.containerd.runc.v2 -           ok
io.containerd.grpc.v1                  cri                   -           ok
```

---

### `containerd.*.stacks.log`

containerd goroutine stack dumps (if present).

- **Source:** Glob of `/tmp/containerd.*.stacks.log` — these files are written by containerd when it receives `SIGUSR1`
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** Go runtime goroutine stack traces for all containerd goroutines at the time of the signal
- **Present only if:** containerd was sent `SIGUSR1` prior to collection
