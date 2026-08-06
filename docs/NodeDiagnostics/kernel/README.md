# kernel/

Kernel ring buffer messages and kernel version information.

**Collector source:** [`pkg/log_collector/collect/kernel.go`](../../../pkg/log_collector/collect/kernel.go)

## Files

### `dmesg.current`

Raw kernel ring buffer output with monotonic timestamps (seconds since boot).

- **Command:** `dmesg` — [`dmesg(1)`](https://man7.org/linux/man-pages/man1/dmesg.1.html)
- **Linux syscall:** [`syslog(2)`](https://man7.org/linux/man-pages/man2/syslog.2.html) (action `SYSLOG_ACTION_READ_ALL`) — the `dmesg` binary reads from the kernel log buffer via this syscall
- **Content:** All kernel messages since boot: hardware detection, driver init, filesystem mounts, network adapter bring-up, SELinux policy load, systemd early boot messages

**Sample output (truncated):**
```
[    0.000000] Booting Linux on physical CPU 0x0000000000 [0x413fd0c1]
[    0.000000] Linux version 6.12.73 (builder@buildkitsandbox) ... #1 SMP Fri Mar  6 02:11:22 UTC 2026
[    0.000000] KASLR enabled
[    0.000000] efi: EFI v2.7 by EDK II
[    0.000000] ACPI: RSDP 0x00000000786E0014 000024 (v02 AMAZON)
[    0.000000] DMI: Amazon EC2 c6g.xlarge/, BIOS 1.0 11/1/2018
[    0.000000] Zone ranges:
[    0.000000]   DMA      [mem 0x0000000040000000-0x00000000ffffffff]
[    0.000000]   Normal   [mem 0x0000000100000000-0x00000005b5ffffff]
[    0.000000] Memory: 7875360K/8224768K available
[    0.000000] SELinux:  Initializing.
[    2.353125] ena 0000:00:05.0: Elastic Network Adapter (ENA) v2.16.1g
[    2.475839] ena 0000:00:05.0: Elastic Network Adapter (ENA) found at mem 80114000, mac addr 02:a7:62:7b:88:09
[    3.416083] XFS (nvme1n1p1): Mounting V5 Filesystem 4e59024a-36a7-4069-b55e-262a43fc9c9c
[    3.455757] XFS (nvme1n1p1): Ending clean mount
[   11.960223] eni1d44c52eb2c: Caught tx_queue_len zero misconfig
```

Key things to look for:
- `KASLR enabled` — kernel address space layout randomization active
- `ENA` lines — Elastic Network Adapter driver initialization
- `XFS` lines — data volume mount status
- `tx_queue_len zero misconfig` — ENI virtual interfaces attached by VPC CNI (normal)
- Any `BUG:`, `WARNING:`, `Oops:`, `soft lockup`, or `hung_task` messages indicate kernel issues

---

### `dmesg.human.current`

Same kernel ring buffer as `dmesg.current` but with human-readable wall-clock timestamps (`--ctime` flag).

- **Command:** `dmesg --ctime` — [`dmesg(1)`](https://man7.org/linux/man-pages/man1/dmesg.1.html)
- **Linux syscall:** [`syslog(2)`](https://man7.org/linux/man-pages/man2/syslog.2.html)
- **Content:** Identical to `dmesg.current` but timestamps are converted to calendar time using the system clock at collection time

**Sample output (truncated):**
```
[Wed Mar 18 22:26:04 2026] Booting Linux on physical CPU 0x0000000000 [0x413fd0c1]
[Wed Mar 18 22:26:04 2026] Linux version 6.12.73 ...
[Wed Mar 18 22:26:04 2026] KASLR enabled
[Wed Mar 18 22:26:04 2026] SELinux:  Initializing.
[Wed Mar 18 22:26:06 2026] ena 0000:00:05.0: Elastic Network Adapter (ENA) v2.16.1g
[Wed Mar 18 22:26:07 2026] XFS (nvme1n1p1): Mounting V5 Filesystem 4e59024a-36a7-4069-b55e-262a43fc9c9c
[Wed Mar 18 22:26:07 2026] XFS (nvme1n1p1): Ending clean mount
[Wed Mar 18 22:26:16 2026] eni1d44c52eb2c: Caught tx_queue_len zero misconfig
```

Use this file when correlating kernel events with application-level timestamps.

---

### `dmesg.boot`

Copy of `/var/log/dmesg` — the kernel ring buffer snapshot saved at boot time by some distributions (e.g., Amazon Linux 2). Not present on Bottlerocket.

- **Source:** `/var/log/dmesg` (file copy via `os.CopyFile`)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html), [`write(2)`](https://man7.org/linux/man-pages/man2/write.2.html)
- **Content:** Kernel messages from the previous boot cycle, useful for diagnosing boot failures

---

### `uname.txt`

Kernel version, hostname, and architecture string.

- **Command:** `uname -a` — [`uname(1)`](https://man7.org/linux/man-pages/man1/uname.1.html)
- **Linux syscall:** [`uname(2)`](https://man7.org/linux/man-pages/man2/uname.2.html)
- **Content:** Kernel release, build date, machine hardware name

**Sample output:**
```
Linux ip-192-168-xxx-xxx.eu-west-1.compute.internal 6.12.73 #1 SMP Fri Mar  6 02:11:22 UTC 2026 aarch64 GNU/Linux
```

Fields: `<hostname> <kernel-release> <kernel-version> <machine> <OS>`

---

### `modinfo/<module-name>`

Module information for selected kernel modules (currently: `lustre`).

- **Command:** `modinfo <module>` — [`modinfo(8)`](https://man7.org/linux/man-pages/man8/modinfo.8.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/lib/modules/<version>/modules.dep`
- **Content:** Module filename, description, license, version, dependencies
- **Note:** Collection failures are silently ignored — the module may simply not be present on the node
