# sysctls/

All kernel runtime parameters.

**Collector source:** [`pkg/log_collector/collect/system.go` – `sysctl()`](../../../pkg/log_collector/collect/system.go)

---

## Files

### `sysctl_all.txt`

All kernel parameters and their current values.

- **Command:** `sysctl --all` — [`sysctl(8)`](https://man7.org/linux/man-pages/man8/sysctl.8.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) on files under `/proc/sys/` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html) (sysctl reads the procfs pseudo-filesystem)
- **Content:** Every tunable kernel parameter in `key = value` format, organized by subsystem prefix

**Sample output (truncated):**
```
abi.vsyscall32 = 1
debug.exception-trace = 1
fs.aio-max-nr = 65536
fs.file-max = 9223372036854775807
fs.inotify.max_user_watches = 8192
kernel.hostname = ip-192-168-xxx-xxx.eu-west-1.compute.internal
kernel.pid_max = 32768
kernel.threads-max = 62500
net.core.rmem_max = 212992
net.core.somaxconn = 4096
net.ipv4.conf.all.forwarding = 1
net.ipv4.ip_forward = 1
net.ipv4.tcp_keepalive_time = 7200
net.ipv4.tcp_max_syn_backlog = 4096
net.ipv6.conf.all.forwarding = 1
vm.max_map_count = 65530
vm.swappiness = 60
```

Key parameters to check for EKS node health:
- `net.ipv4.ip_forward = 1` — required for pod networking
- `net.ipv4.conf.all.forwarding = 1` — required for pod-to-pod traffic
- `fs.inotify.max_user_watches` — low values can cause issues with file watchers in pods
- `kernel.pid_max` — if near the limit, new processes cannot be created
- `fs.file-max` — global file descriptor limit
- `net.netfilter.nf_conntrack_max` — conntrack table size; exhaustion causes dropped connections
- `vm.max_map_count` — required by Elasticsearch and similar workloads
