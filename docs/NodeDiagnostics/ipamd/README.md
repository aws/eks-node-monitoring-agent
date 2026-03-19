# ipamd/

VPC CNI IPAMD (IP Address Management Daemon) introspection data and checkpoint state.

**Collector source:** [`pkg/log_collector/collect/ipamd.go`](../../../pkg/log_collector/collect/ipamd.go)

Data is collected from two local HTTP endpoints exposed by the `aws-node` DaemonSet:
- `http://localhost:61679/v1/` — introspection API
- `http://localhost:61678/` — Prometheus metrics

**Not collected on:** Hybrid nodes (IPAMD/VPC CNI is not installed).

---

## Files

### `enis.json`

All ENIs (Elastic Network Interfaces) attached to the node and their IP allocations.

- **Source:** HTTP GET `http://localhost:61679/v1/enis`
- **Linux syscall:** [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) + [`sendto(2)`](https://man7.org/linux/man-pages/man2/sendto.2.html) (TCP to localhost) — see [`socket(2)`](https://man7.org/linux/man-pages/man2/socket.2.html)
- **Content:** Per-ENI details including ENI ID, MAC address, subnet, security groups, and the list of IPv4/IPv6 addresses assigned to each ENI

**Sample output (truncated):**
```json
{
  "AssignedIPs": 5,
  "ENIs": {
    "eni-<eni-id>": {
      "ENIID": "eni-<eni-id>",
      "MAC": "02:a7:62:7b:88:09",
      "SubnetIPv4CIDR": "192.168.128.0/18",
      "IPv4Addresses": {
        "192.168.152.126": {"Address": "192.168.152.126", "Assigned": true, "UnassignedTime": "0001-01-01T00:00:00Z"},
        "192.168.152.64":  {"Address": "192.168.152.64",  "Assigned": true, "UnassignedTime": "0001-01-01T00:00:00Z"}
      }
    }
  }
}
```

---

### `pods.json`

Pod-to-IP mapping as tracked by IPAMD.

- **Source:** HTTP GET `http://localhost:61679/v1/pods`
- **Content:** Each pod's namespace, name, and assigned IP address

**Sample output (truncated):**
```json
{
  "kube-system/aws-node-xxxxx": {
    "PodName": "aws-node-xxxxx",
    "PodNamespace": "kube-system",
    "PodIP": "192.168.152.126"
  }
}
```

---

### `networkutils-env-settings.json`

Environment variable settings for the `aws-node` network utilities component.

- **Source:** HTTP GET `http://localhost:61679/v1/networkutils-env-settings`
- **Content:** Key-value pairs of environment variables controlling network utility behavior (e.g., `AWS_VPC_K8S_CNI_EXTERNALSNAT`, `AWS_VPC_ENI_MTU`)

**Sample output:**
```json
{
  "AWS_VPC_K8S_CNI_EXTERNALSNAT": "false",
  "AWS_VPC_ENI_MTU": "9001",
  "AWS_VPC_K8S_CNI_RANDOMIZESNAT": "prng"
}
```

---

### `ipamd-env-settings.json`

Environment variable settings for the IPAMD component.

- **Source:** HTTP GET `http://localhost:61679/v1/ipamd-env-settings`
- **Content:** IPAMD configuration variables (e.g., `WARM_IP_TARGET`, `MINIMUM_IP_TARGET`, `ENABLE_PREFIX_DELEGATION`)

**Sample output:**
```json
{
  "WARM_IP_TARGET": "2",
  "MINIMUM_IP_TARGET": "3",
  "ENABLE_PREFIX_DELEGATION": "false",
  "MAX_ENI": "4"
}
```

---

### `eni-configs.json`

ENIConfig custom resource data (used with custom networking).

- **Source:** HTTP GET `http://localhost:61679/v1/eni-configs`
- **Content:** ENIConfig objects if custom networking is enabled; empty object `{}` otherwise

---

### `metrics.json`

Prometheus metrics from IPAMD.

- **Source:** HTTP GET `http://localhost:61678/metrics`
- **Content:** All IPAMD Prometheus metrics in text exposition format, including IP allocation counts, ENI attachment counts, API call latencies, and error counters

**Sample output (truncated):**
```
# HELP awscni_assigned_ip_addresses The number of IP addresses assigned to pods
# TYPE awscni_assigned_ip_addresses gauge
awscni_assigned_ip_addresses 4
# HELP awscni_total_ip_addresses The total number of IP addresses
# TYPE awscni_total_ip_addresses gauge
awscni_total_ip_addresses 14
# HELP awscni_eni_allocated The number of ENIs allocated
# TYPE awscni_eni_allocated gauge
awscni_eni_allocated 2
```

---

### `ipam.json`

IPAMD checkpoint file — persisted IP allocation state.

- **Source:** File copy of `/var/run/aws-node/ipam.json` (or `/run/aws-node/ipam.json` on Bottlerocket)
- **Linux syscall:** [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html), [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** Serialized IPAMD state including allocated IPs and their pod assignments, used by IPAMD to recover state after a restart without re-querying the EC2 API

**Sample output (truncated):**
```json
{
  "allocations": {
    "192.168.152.64": {
      "podName": "coredns-fd7d56586-xxxxx",
      "podNamespace": "kube-system",
      "ifName": "eth0",
      "sandboxID": "..."
    }
  }
}
```
