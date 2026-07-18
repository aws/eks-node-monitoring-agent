# cni/

CNI plugin configuration files and VPC CNI container environment variables.

**Collector source:** [`pkg/log_collector/collect/cni.go`](../../../pkg/log_collector/collect/cni.go)

---

## Files

### `10-aws.conflist`

The AWS VPC CNI plugin configuration file.

- **Source:** Directory copy of `/etc/cni/net.d/` via `cniConfig()` — copies all files from the CNI config directory
- **Linux syscall:** [`getdents64(2)`](https://man7.org/linux/man-pages/man2/getdents64.2.html) + [`open(2)`](https://man7.org/linux/man-pages/man2/open.2.html) + [`read(2)`](https://man7.org/linux/man-pages/man2/read.2.html)
- **Content:** CNI plugin chain configuration in JSON format, specifying the VPC CNI plugin, bandwidth plugin, and portmap plugin with their settings

**Sample output:**
```json
{
  "cniVersion": "1.1.0",
  "name": "aws-cni",
  "disableCheck": true,
  "plugins": [
    {
      "name": "aws-cni",
      "type": "aws-cni",
      "vethPrefix": "eni",
      "mtu": "9001",
      "pluginLogFile": "/var/log/aws-routed-eni/plugin.log",
      "pluginLogLevel": "DEBUG"
    },
    {
      "type": "portmap",
      "capabilities": {"portMappings": true}
    },
    {
      "type": "bandwidth",
      "capabilities": {"bandwidth": true}
    }
  ]
}
```

---

### `cni-configuration-variables-containerd.json`

Environment variables and configuration of the running `amazon-k8s-cni` container.

- **Source:** [`cni.go` – `cniVariables()`](../../../pkg/log_collector/collect/cni.go) — lists containers via `ctr --namespace k8s.io container list`, finds the `amazon-k8s-cni:v*` container, then runs `ctr --namespace k8s.io container info <id>`
- **Linux syscall:** gRPC over [`connect(2)`](https://man7.org/linux/man-pages/man2/connect.2.html) on `/run/containerd/containerd.sock`
- **Content:** Full containerd container spec including environment variables, mounts, and OCI runtime config for the VPC CNI container
- **Present only if:** The `amazon-k8s-cni` container is running in the `k8s.io` namespace

**Sample output (truncated):**
```json
{
  "ID": "...",
  "Image": "602401143452.dkr.ecr.eu-west-1.amazonaws.com/amazon-k8s-cni:v1.19.x",
  "Spec": {
    "process": {
      "env": [
        "AWS_VPC_K8S_CNI_LOGLEVEL=DEBUG",
        "AWS_VPC_ENI_MTU=9001",
        "ENABLE_PREFIX_DELEGATION=false",
        "WARM_PREFIX_TARGET=1"
      ]
    }
  }
}
```
