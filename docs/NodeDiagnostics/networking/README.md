# networking/

Network interface configuration, routing tables, firewall rules, connection tracking, DNS resolver config, and API server connectivity.

**Collector sources:**
- [`pkg/log_collector/collect/networking.go`](../../../pkg/log_collector/collect/networking.go) — interfaces, routes, conntrack, resolv.conf, API server check
- [`pkg/log_collector/collect/iptables.go`](../../../pkg/log_collector/collect/iptables.go) — iptables/ip6tables rules
- [`pkg/log_collector/collect/nftables.go`](../../../pkg/log_collector/collect/nftables.go) — nftables rules

---

## Files

### `ethtool.txt`

NIC statistics for every network interface on the node.

- **Source:** [`networking.go` – `interfaces()`](../../../pkg/log_collector/collect/networking.go) — enumerates interfaces via `net.Interfaces()` (calls [`getifaddrs(3)`](https://man7.org/linux/man-pages/man3/getifaddrs.3.html) / [`ioctl(2)`](https://man7.org/linux/man-pages/man2/ioctl.2.html) `SIOCGIFCONF`), then runs `ethtool -S <iface>` — [`ethtool(8)`](https://man7.org/linux/man-pages/man8/ethtool.8.html) for each
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) + [`ioctl(2)`](https://man7.org/linux/man-pages/man2/ioctl.2.html) with `SIOCGIFCONF`; [`ioctl(2)`](https://man7.org/linux/man-pages/man2/ioctl.2.html) with `ETHTOOL_GSTATS` for ethtool stats
- **Content:** Per-interface driver statistics (tx/rx packets, bytes, errors, drops, queue stats). Each interface is separated by a header line.

**Sample output (truncated):**
```
Interface eth0
NIC statistics:
     tx_timeout: 0
     suspend: 0
     resume: 0
     wd_expired: 0
     interface_up: 1
     interface_down: 0
     admin_q_pause: 0
     queue_stop: 0
     queue_wakeup: 0
     rx_drops: 0
     tx_drops: 0
     ...

Interface eni1d44c52eb2c
NIC statistics:
     tx_timeout: 0
     rx_drops: 0
     ...
```

---

### `iprule.txt`

IPv4 routing policy rules.

- **Command:** `ip rule show` — [`ip-rule(8)`](https://man7.org/linux/man-pages/man8/ip-rule.8.html)
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) + [`sendmsg(2)`](https://man7.org/linux/man-pages/man2/sendmsg.2.html) on `AF_NETLINK` with `RTM_GETRULE` — see [`netlink(7)`](https://man7.org/linux/man-pages/man7/netlink.7.html), [`rtnetlink(7)`](https://man7.org/linux/man-pages/man7/rtnetlink.7.html)
- **Content:** Policy routing rules in priority order. VPC CNI adds rules per ENI to route traffic through the correct interface.

**Sample output:**
```
0:	lookup local
512:	from all lookup main
1024:	from all lookup 512
```

---

### `ip6rule.txt`

IPv6 routing policy rules.

- **Command:** `ip -6 rule show` — [`ip-rule(8)`](https://man7.org/linux/man-pages/man8/ip-rule.8.html)
- **Linux syscall:** [`AF_NETLINK`](https://man7.org/linux/man-pages/man7/netlink.7.html) `RTM_GETRULE` with `AF_INET6`

---

### `iproute.txt`

All IPv4 routing tables.

- **Command:** `ip route show table all` — [`ip-route(8)`](https://man7.org/linux/man-pages/man8/ip-route.8.html)
- **Linux syscall:** [`AF_NETLINK`](https://man7.org/linux/man-pages/man7/netlink.7.html) `RTM_GETROUTE` — see [`rtnetlink(7)`](https://man7.org/linux/man-pages/man7/rtnetlink.7.html)
- **Content:** All routes across all routing tables (main, local, and VPC CNI per-ENI tables)

**Sample output (truncated):**
```
default via 192.168.128.1 dev eth0
192.168.128.0/18 dev eth0 proto kernel scope link src 192.168.152.126
192.168.152.64/26 dev eni1d44c52eb2c scope link
broadcast 192.168.128.0 dev eth0 table local proto kernel scope link src 192.168.152.126
```

---

### `ip6route.txt`

All IPv6 routing tables.

- **Command:** `ip -6 route show table all` — [`ip-route(8)`](https://man7.org/linux/man-pages/man8/ip-route.8.html)
- **Linux syscall:** [`AF_NETLINK`](https://man7.org/linux/man-pages/man7/netlink.7.html) `RTM_GETROUTE` with `AF_INET6`

---

### `conntrack.txt`

Connection tracking table (IPv4) — both statistics and active connections.

- **Source:** [`networking.go` – `conntrack()`](../../../pkg/log_collector/collect/networking.go)
- **Commands:** `conntrack -S` — [`conntrack(8)`](https://man7.org/linux/man-pages/man8/conntrack.8.html) (statistics) then `conntrack -L` (connection list), appended
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) on `AF_NETLINK` with `NETLINK_NETFILTER`; [`sendmsg(2)`](https://man7.org/linux/man-pages/man2/sendmsg.2.html) with `NFNL_SUBSYS_CTNETLINK` — see [`netlink(7)`](https://man7.org/linux/man-pages/man7/netlink.7.html)
- **Content:** Per-CPU conntrack statistics followed by all tracked connections with state, protocol, timeout, src/dst addresses

**Sample output:**
```
*** Output of conntrack -S ***
cpu=0           found=0 invalid=0 insert=0 insert_failed=0 drop=0 early_drop=0 error=0 search_restart=0
cpu=1           found=0 invalid=0 ...

*** Output of conntrack -L ***
tcp      6 86399 ESTABLISHED src=192.168.152.126 dst=10.100.0.1 sport=45678 dport=443 ...
udp      17 29 src=192.168.152.126 dst=192.168.0.2 sport=53 dport=53 ...
```

---

### `conntrack6.txt`

IPv6 connection tracking table.

- **Command:** `conntrack -L -f ipv6` — [`conntrack(8)`](https://man7.org/linux/man-pages/man8/conntrack.8.html)
- **Linux syscall:** [`AF_NETLINK`](https://man7.org/linux/man-pages/man7/netlink.7.html) `NETLINK_NETFILTER`

---

### `resolv.conf`

DNS resolver configuration.

- **Source:** File copy of `/etc/resolv.conf`
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** `nameserver`, `search`, and `options` directives — see [`resolv.conf(5)`](https://man7.org/linux/man-pages/man5/resolv.conf.5.html)

**Sample output:**
```
nameserver 192.168.0.2
search eu-west-1.compute.internal
options ndots:5
```

---

### `configure-multicard-interfaces.txt`

Journal log for the `configure-multicard-interfaces` systemd service.

- **Command:** `journalctl -o short-iso-precise -u configure-multicard-interfaces` — [`journalctl(1)`](https://man7.org/linux/man-pages/man1/journalctl.1.html)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) on `/run/log/journal/` or [`AF_UNIX`](https://man7.org/linux/man-pages/man7/unix.7.html) socket to `systemd-journald`
- **Content:** Log output from the service that configures secondary ENI routing on multi-card instances

---

### `get_api_server.txt`

Result of an HTTPS GET to the Kubernetes API server `/livez?verbose` endpoint.

- **Source:** [`networking.go` – `apiServerConnectivity()`](../../../pkg/log_collector/collect/networking.go) — reads the API server URL from the node's kubeconfig, builds an HTTP client with the cluster CA cert, and performs the request
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) + [`sendto(2)`](https://man7.org/linux/man-pages/man2/sendto.2.html) (TLS over TCP)
- **Content:** The request URL and the API server's liveness response

**Sample output:**
```
sending GET request to https://XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX.gr7.eu-west-1.eks.amazonaws.com/livez?verbose
[+]ping ok
[+]log ok
[+]etcd ok
ok
```

---

### `ifconfig.txt`

Network interface addresses and statistics (legacy format).

- **Command:** `ifconfig` — [`ifconfig(8)`](https://man7.org/linux/man-pages/man8/ifconfig.8.html)
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) + [`ioctl(2)`](https://man7.org/linux/man-pages/man2/ioctl.2.html) with `SIOCGIFCONF`, `SIOCGIFFLAGS`, `SIOCGIFADDR`
- **Not collected on:** Bottlerocket

---

### `iptables-filter.txt`, `iptables-mangle.txt`, `iptables-nat.txt`

IPv4 iptables rules per table with rule counts.

- **Source:** [`iptables.go` – `collectRules()`](../../../pkg/log_collector/collect/iptables.go)
- **Command:** `iptables --wait 1 --numeric --verbose --list --table <table>` — [`iptables(8)`](https://man7.org/linux/man-pages/man8/iptables.8.html)
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) + [`getsockopt(2)`](https://man7.org/linux/man-pages/man2/getsockopt.2.html) with `IPT_SO_GET_INFO` and `IPT_SO_GET_ENTRIES`
- **Content:** All chains and rules in the table, plus a total rule count appended at the end

**Sample output (truncated):**
```
Chain INPUT (policy ACCEPT 0 packets, 0 bytes)
 pkts bytes target     prot opt in     out     source               destination
    0     0 KUBE-NODEPORTS  all  --  *      *       0.0.0.0/0            0.0.0.0/0

Chain FORWARD (policy ACCEPT 0 packets, 0 bytes)
...
=======
Total Number of Rules: 47
```

---

### `ip6tables-filter.txt`, `ip6tables-mangle.txt`, `ip6tables-nat.txt`

IPv6 ip6tables rules per table. Same structure as IPv4 counterparts.

---

### `iptables.txt`, `ip6tables.txt`

All iptables/ip6tables rules across all tables combined.

- **Command:** `iptables --wait 1 --numeric --verbose --list` — [`iptables(8)`](https://man7.org/linux/man-pages/man8/iptables.8.html)

---

### `iptables-save.txt`, `ip6tables-save.txt`

Machine-readable iptables rule dump suitable for `iptables-restore`.

- **Command:** `iptables-save` — [`iptables-save(8)`](https://man7.org/linux/man-pages/man8/iptables-save.8.html) / `ip6tables-save` — [`ip6tables-save(8)`](https://man7.org/linux/man-pages/man8/ip6tables-save.8.html)
- **Linux syscall:** [`getsockopt(2)`](https://man7.org/linux/man-pages/man2/getsockopt.2.html) with `IPT_SO_GET_INFO`

---

### `nftables-ip-filter.txt`, `nftables-ip-mangle.txt`, `nftables-ip-nat.txt`

nftables rules for IPv4 tables (filter, mangle, nat).

- **Source:** [`nftables.go`](../../../pkg/log_collector/collect/nftables.go) — lists tables via `nft list tables`, then dumps each with `nft list table <family> <name>`
- **Linux syscall:** [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html) on `AF_NETLINK` with `NETLINK_NETFILTER`; [`sendmsg(2)`](https://man7.org/linux/man-pages/man2/sendmsg.2.html) with `NFNL_SUBSYS_NFTABLES` — see [`netlink(7)`](https://man7.org/linux/man-pages/man7/netlink.7.html)
- **Content:** Full nftables ruleset for the table
- **Skipped if:** `nft --version` fails (binary not present)

---

### `nftables-ip6-filter.txt`, `nftables-ip6-mangle.txt`, `nftables-ip6-nat.txt`

nftables rules for IPv6 tables.

---

### `systemd-network/`

Systemd network link configuration files for active interfaces.

- **Source:** [`networking.go` – `systemdNetworkConfig()`](../../../pkg/log_collector/collect/networking.go) — runs `networkctl` to get active interfaces with a `LinkFile`, then `systemd-analyze cat-config <LinkFile>` for each unique link file
- **Content:** The resolved and merged content of the `.link` file governing each network interface's MAC address policy and other link settings
