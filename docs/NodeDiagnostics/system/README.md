# system/

Node-level system state: processes, CPU/IO throttling, instance identity, region, and systemd services.

**Collector sources:**
- [`pkg/log_collector/collect/system.go`](../../../pkg/log_collector/collect/system.go) — `top`, `ps`, `procs`, `sysctl`, `systemd`, `pkgs`, `reboots`
- [`pkg/log_collector/collect/throttles.go`](../../../pkg/log_collector/collect/throttles.go) — `cpuThrottles`, `ioThrottles`
- [`pkg/log_collector/collect/instance.go`](../../../pkg/log_collector/collect/instance.go) — instance ID
- [`pkg/log_collector/collect/region.go`](../../../pkg/log_collector/collect/region.go) — region and AZ
- [`pkg/log_collector/system/top.go`](../../../pkg/log_collector/system/top.go) — `top` snapshot

---

## Files

### `top.txt`

Snapshot of system resource usage: CPU, memory, and per-process stats.

- **Source:** [`pkg/log_collector/system/top.go`](../../../pkg/log_collector/system/top.go) — calls `top -b -n 1 -w 512` — [`top(1)`](https://man7.org/linux/man-pages/man1/top.1.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/proc/stat`, `/proc/meminfo`, `/proc/[pid]/stat` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** Load average, task counts, per-CPU utilization, memory summary, and a process table sorted by CPU time

**Sample output (truncated):**
```
top - 06:23:56  07:57, load average: 0.06, 0.09, 0.08
Tasks: 171 total, 0 running, 105 sleeping, 0 stopped, 0 zombie
%Cpu(s): 2.2 us, 0.7sy, 0.0 ni, 96.8 id, 0.2 wa, 0.0 hi, 0.0 si, 0.0 st
KiB Mem : 7950200 Total, 4676852 free, 1066836 used, 2399488 buffers/cache
KiB Swap: 1073737728 total, 1073737728 free, 0 used.
PID    USER             NI   VIRT     RES     %CPU  %MEM  TIME+    COMMAND
1844   root             0    1778832  93380   1.3   1.2   6:10.86  kubelet
1730   root             0    1846676  68104   0.4   0.9   2:03.67  containerd
1717   root             0    2159384  116000  0.3   1.5   1:11.90  eks-node-monitoring-agent
```

---

### `ps.txt`

Full process list with forest view showing parent-child relationships.

- **Command:** `ps fauxwww --headers` — [`ps(1)`](https://man7.org/linux/man-pages/man1/ps.1.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/proc/[pid]/stat`, `/proc/[pid]/cmdline`, `/proc/[pid]/status` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** All processes with USER, PID, CPU%, MEM%, VSZ, RSS, TTY, STAT, START, TIME, and full command line

**Sample output (truncated):**
```
USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root           1  0.2  0.1  19904 14316 ?        Ss   Mar18   1:08 /sbin/init ...
root        1717  0.2  1.4 2159384 116200 ?      Ssl  Mar18   1:11 /usr/bin/eks-node-monitoring-agent \
                                                                     --hostname-override i-<instance-id> ...
root        1844  1.2  1.1 1778832 93380 ?       Ssl  Mar18   6:10 /usr/bin/kubelet \
                                                                     --hostname-override i-<instance-id> ...
root        1730  0.4  0.8 1846676 68104 ?       Ssl  Mar18   2:03 /usr/bin/containerd
```

---

### `ps-threads.txt`

Process list including all threads with scheduling information.

- **Command:** `ps -eTF --headers` — [`ps(1)`](https://man7.org/linux/man-pages/man1/ps.1.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/proc/[pid]/task/[tid]/stat` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** All threads (LWP column), SPID, NLWP, and full command line — useful for diagnosing thread-level CPU consumption

---

### `procstat.txt`

Kernel-level CPU and interrupt statistics from `/proc/stat`.

- **Source:** Direct file copy of `/proc/stat`
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) on `/proc/stat` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** Aggregate and per-CPU time in user/system/idle/iowait/irq/softirq modes, interrupt counts, context switches, boot time, process counts

**Sample output:**
```
cpu  82680 0 47403 11296780 26042 0 2758 0 0 0
cpu0 20750 0 12327 2818779 7094 0 629 0 0 0
cpu1 20585 0 11588 2825673 6558 0 737 0 0 0
cpu2 20749 0 11634 2826133 6247 0 683 0 0 0
cpu3 20596 0 11854 2826193 6140 0 709 0 0 0
intr 43106267 0 303161 15811318 ...
ctxt 72519175
btime 1773872764
processes 26639
procs_running 1
procs_blocked 0
softirq 9821788 0 2281194 16 1562595 ...
```

Fields: `user nice system idle iowait irq softirq steal guest guest_nice` (in jiffies)

---

### `allprocstat.txt`

Per-process `/proc/[pid]/stat` entries concatenated for all running processes.

- **Source:** Glob of `/proc/[0-9]*/stat`, read via `open(2)` + `read(2)`
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html), [`getdents64(2)`](https://man7.org/linux/man-pages/man2/getdents64.2.html) (directory listing)
- **Content:** Raw stat fields for every process: PID, comm, state, ppid, pgrp, session, tty, utime, stime, vsize, rss, etc. (52 fields per the [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html) man page)
- **Use:** Programmatic analysis of process state; field 42 (1-indexed) is the aggregated block I/O delay used by `io_throttling.txt`

---

### `cpu_throttling.txt`

Processes that have been CPU-throttled by cgroup CPU limits.

- **Source:** [`pkg/log_collector/collect/throttles.go` – `cpuThrottles()`](../../../pkg/log_collector/collect/throttles.go)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) on `/sys/fs/cgroup/**/cpu.stat` and `cgroup.procs` — see [`cgroups(7)`](https://man7.org/linux/man-pages/man7/cgroups.7.html)
- **Content:** `ps ax` output filtered to only PIDs whose cgroup `cpu.stat` shows `nr_throttled > 0`. If no throttling is detected, the file contains `No CPU Throttling Found`.

**Sample output (no throttling):**
```
No CPU Throttling Found
```

**Sample output (throttling detected):**
```
  1844 ?        Ssl    6:10 /usr/bin/kubelet ...
  2473 ?        Ssl    0:58 /fluent-bit/bin/fluent-bit ...
```

---

### `io_throttling.txt`

Processes experiencing block I/O delays, sorted descending by delay.

- **Source:** [`pkg/log_collector/collect/throttles.go` – `ioThrottles()`](../../../pkg/log_collector/collect/throttles.go)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) on `/proc/[pid]/stat` (field 42: `blkio_ticks`) — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** `PID Name Block IO Delay (centiseconds)` — only processes with non-zero block I/O delay are listed

**Sample output (no I/O delays):**
```
PID Name Block IO Delay (centisconds)
```

**Sample output (delays present):**
```
PID Name Block IO Delay (centisconds)
1844 kubelet 342
1730 containerd 87
```

---

### `instance-id.txt`

EC2 instance ID of the node.

- **Source:** [`pkg/log_collector/collect/instance.go`](../../../pkg/log_collector/collect/instance.go)
- **Primary:** File copy of `/var/lib/cloud/data/instance-id`
- **Fallback:** IMDS HTTP call to `http://169.254.169.254/latest/meta-data/instance-id` via the AWS SDK `imds` client
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html) (file path), or [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) + [`sendto(2)`](https://man7.org/linux/man-pages/man2/sendto.2.html) (IMDS)
- **Not collected on:** Hybrid nodes (no IMDS access)

**Sample output:**
```
i-<instance-id>
```

---

### `region.txt`

AWS region of the node.

- **Source:** [`pkg/log_collector/collect/region.go`](../../../pkg/log_collector/collect/region.go) — IMDS call to `/placement/region`
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) + [`sendto(2)`](https://man7.org/linux/man-pages/man2/sendto.2.html) (HTTP to IMDS link-local address)
- **Not collected on:** Hybrid nodes

**Sample output:**
```
eu-west-1
```

---

### `availability-zone.txt`

AWS Availability Zone of the node.

- **Source:** [`pkg/log_collector/collect/region.go`](../../../pkg/log_collector/collect/region.go) — IMDS call to `/placement/availability-zone`
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) + [`sendto(2)`](https://man7.org/linux/man-pages/man2/sendto.2.html)
- **Not collected on:** Hybrid nodes

**Sample output:**
```
eu-west-1a
```

---

### `services.txt`

All loaded systemd units and their states.

- **Command:** `systemctl list-units` — [`systemctl(1)`](https://man7.org/linux/man-pages/man1/systemctl.1.html)
- **Linux syscall:** D-Bus socket communication with `systemd` PID 1 via [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) on `/run/systemd/private/io.systemd.Manager` — see [`sd-bus(3)`](https://man7.org/linux/man-pages/man3/sd-bus.3.html)
- **Content:** Unit name, LOAD, ACTIVE, SUB state, and description for every loaded unit
- **Not collected on:** Bottlerocket (unless EKS Auto Mode)

**Sample output (truncated):**
```
  UNIT                                    LOAD   ACTIVE  SUB     DESCRIPTION
  containerd.service                      loaded active  running containerd container runtime
  kubelet.service                         loaded active  running Kubelet
  eks-node-monitoring-agent.service       loaded active  running eks node monitoring agent
  ipamd.service                           loaded active  running aws k8s agent IPAMD
  coredns.service                         loaded active  running CoreDNS DNS server
  kube-proxy.service                      loaded active  running kube-proxy
  eks-healthchecker.service               loaded active  running EKS Health Checker
  eks-pod-identity-agent.service          loaded active  running EKS Pod Identity Agent
  eks-ebs-csi-driver.service              loaded active  running AWS EBS CSI Driver
  chronyd.service                         loaded active  running Network Time Protocol
  systemd-networkd.service                loaded active  running Network Configuration
  systemd-resolved.service                loaded active  running Network Name Resolution
232 loaded units listed.
```

---

### `systemd-analyze.svg`

SVG visualization of the systemd boot sequence and service startup times.

- **Command:** `systemd-analyze plot` — [`systemd-analyze(1)`](https://man7.org/linux/man-pages/man1/systemd-analyze.1.html)
- **Linux syscall:** D-Bus socket communication with systemd — see [`sd-bus(3)`](https://man7.org/linux/man-pages/man3/sd-bus.3.html)
- **Content:** A Gantt-chart SVG showing each unit's activation time relative to boot
- **Not collected on:** Bottlerocket (unless EKS Auto Mode)

---

### `netstat.txt`

Active network connections and listening sockets.

- **Command:** `netstat -plant` — [`netstat(8)`](https://man7.org/linux/man-pages/man8/netstat.8.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/proc/net/tcp`, `/proc/net/tcp6`, `/proc/net/udp` — see [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)
- **Content:** Protocol, local/foreign address, state, PID/program name
- **Not collected on:** Bottlerocket

---

### `pkglist.txt`

Installed package list.

- **Command:** `rpm -qa` — [`rpm(8)`](https://man7.org/linux/man-pages/man8/rpm.8.html) (RPM-based) or `deb --list` (Debian-based)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on RPM/dpkg database files
- **Content:** All installed packages with version and architecture
- **Not collected on:** Bottlerocket

---

### `last_reboot.txt`

Recent reboot history from the `wtmp` log.

- **Command:** `last reboot` — [`last(1)`](https://man7.org/linux/man-pages/man1/last.1.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/var/log/wtmp` — see [`utmp(5)`](https://man7.org/linux/man-pages/man5/utmp.5.html)
- **Content:** Timestamps of previous reboots
- **Not collected on:** Bottlerocket

---

### `selinux.txt`

Current SELinux enforcement mode.

- **Source:** [`pkg/log_collector/collect/selinux.go`](../../../pkg/log_collector/collect/selinux.go) — reads the `selinuxfs` mountpoint's `enforce` file
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/sys/fs/selinux/enforce` (or equivalent mountpoint) — see [`selinux(8)`](https://man7.org/linux/man-pages/man8/selinux.8.html)
- **Content:** One of: `SELinux mode: Enforcing`, `SELinux mode: Permissive`, or `SELinux mode: Disabled (no mountpoint)`
- **Not collected on:** Bottlerocket
