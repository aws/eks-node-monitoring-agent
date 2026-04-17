# kubectl-ekslogs

A kubectl plugin to collect diagnostic log bundles from Amazon EKS nodes using the [NodeDiagnostic](https://docs.aws.amazon.com/eks/latest/userguide/node-health.html) API.

## Overview

Troubleshooting EKS node issues often requires gathering system logs, kubelet output, and other diagnostic data from the underlying EC2 instance. `kubectl ekslogs` automates this workflow as it creates a `NodeDiagnostic` resource, waits for log collection to complete, and downloads the resulting tarball directly to your local machine. No SSH access or SSM sessions required.

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/) configured with access to an EKS cluster
- The EKS Node Monitoring Agent Daemonset must be installed in your cluster
- Sufficient RBAC permissions to create/delete `NodeDiagnostic` resources

## Installation

### Manual

Download the plugin and place it in your `PATH`:

```bash
curl -LO https://raw.githubusercontent.com/aws/eks-node-monitoring-agent/refs/heads/main/tools/kubectl-ekslogs/kubectl-ekslogs
chmod +x kubectl-ekslogs
sudo mv kubectl-ekslogs /usr/local/bin/
```


## Usage
kubectl ekslogs [FLAGS] <node> [node...]

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-l`, `--selector <selector>` | Select nodes by label (same syntax as `kubectl -l`) | — |
| `-t`, `--timeout <duration>` | Timeout for log collection | `300s` |
| `-o`, `--output-dir <path>` | Directory to save log bundles | `.` (current directory) |
| `--s3 <bucket-name>` | Upload logs to S3 bucket instead of downloading locally | — |
| `--key <s3-key-prefix>` | S3 key prefix for logs (requires `--s3`) | root of bucket |
| `--no-proxy` | Use a debug pod and `kubectl cp` instead of the node proxy API | `false` |
| `--debug-image <image>` | Container image for debug pod (required with `--no-proxy`) | - |
| `-h`, `--help` | Show help message | — |


### Examples

**Collect logs from a single node:**
```bash
kubectl ekslogs ip-10-0-1-42.ec2.internal
```
**Collect logs from multiple nodes:**
```bash
kubectl ekslogs ip-10-0-1-42.ec2.internal ip-10-0-1-43.ec2.internal
```
**Collect logs from all nodes in a node group:**
```bash
kubectl ekslogs -l eks.amazonaws.com/nodegroup=my-node-group
```
**Collect logs from all nodes with a specific instance type:**
```bash
kubectl ekslogs -l node.kubernetes.io/instance-type=m5.xlarge
```
**Upload logs to S3 bucket (root directory):**
```bash
kubectl ekslogs --s3 my-bucket ip-10-0-1-42.ec2.internal
```
**Upload logs to S3 with custom key prefix:**
```bash
kubectl ekslogs --s3 my-bucket --key logs/2024-03 ip-10-0-1-42.ec2.internal
```
**Use debug pod transfer (no node proxy API required):**
```bash
kubectl ekslogs --no-proxy --debug-image public.ecr.aws/docker/library/busybox:stable-musl ip-10-0-1-42.ec2.internal
```
**Collect logs with a custom timeout and output directory:**
```bash
kubectl ekslogs --timeout 600s --output-dir /tmp/logs ip-10-0-1-42.ec2.internal
```

### Transfer Modes

The plugin supports three transfer modes:

1. **Node Proxy API (default)**: Downloads logs directly via the Kubernetes node proxy API. This is the fastest and most straightforward method.

2. **S3 Upload (`--s3`)**: Uploads logs directly to an S3 bucket using pre-signed URLs. EKS uploads the logs to S3, and the plugin reports the S3 location. Requires AWS credentials configured and `boto3` installed (`pip install boto3`).

3. **Debug Pod (`--no-proxy`)**: Creates a temporary pod on the node with a hostPath volume mount to access logs. Useful when the node proxy API is unavailable or restricted. Requires permissions to create pods with hostPath volumes.

### Sample Output

**Standard download:**
```
⟳ Validating node(s)...
✔ All 2 node(s) validated
✔ Transfer mode: node proxy API
⟳ Creating NodeDiagnostic resources...
⟳ Waiting for log collection to complete (timeout: 300s)...
✔ Log collection completed on all nodes
⟳ Downloading log bundles...
✔ Saved: ./ip-10-0-1-42.ec2.internal-logs.tar.gz (14M)
✔ Saved: ./ip-10-0-1-43.ec2.internal-logs.tar.gz (12M)

✔ Done — 2 log bundle(s) downloaded to ./
```

**S3 upload:**
```
⟳ Validating node(s)...
✔ All 1 node(s) validated
✔ Transfer mode: S3 (bucket: my-bucket)
⟳ Generating pre-signed S3 URLs...
✔ Generated 1 pre-signed URL(s)
⟳ Creating NodeDiagnostic resources...
⟳ Waiting for log collection to complete (timeout: 300s)...
✔ Log collection completed on all nodes
⟳ Downloading log bundles...
⟳ Logs for ip-10-0-1-42.ec2.internal uploaded to s3://my-bucket/logs/2024-03/ip-10-0-1-42.ec2.internal-logs.tar.gz

✔ Done — 1 log bundle(s) uploaded to s3://my-bucket/
```