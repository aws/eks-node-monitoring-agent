# EKS Node Monitoring Agent

This chart installs the [`eks-node-monitoring-agent`](https://github.com/aws/eks-node-monitoring-agent).

## Prerequisites

- Kubernetes v{?} running on AWS
- Helm v3

## Installing the Chart

```shell
# using the github chart repository
helm repo add eks-node-monitoring-agent https://aws.github.io/eks-node-monitoring-agent
helm install eks-node-monitoring-agent eks-node-monitoring-agent/eks-node-monitoring-agent --namespace kube-system
```

**OR**

```shell
# using the chart sources
git clone https://github.com/aws/eks-node-monitoring-agent.git
cd eks-node-monitoring-agent
helm install eks-node-monitoring-agent ./charts/eks-node-monitoring-agent --namespace kube-system
```

To uninstall:

```shell
helm uninstall eks-node-monitoring-agent --namespace kube-system
```

## DCGM host engine

On NVIDIA GPU nodes the agent collects GPU health from DCGM. It connects as a
DCGM client in standalone mode, so an `nv-hostengine` process must be reachable
on the node (`localhost:5555` by default). The chart provides one via the
`dcgm-server` DaemonSet.

`nv-hostengine`, `dcgmi` and the DCGM modules ship inside the
`eks-node-monitoring-agent` image, so `dcgm-server` runs the same image as the
node agent and no separate DCGM image is pulled. Previously this DaemonSet ran
`eks/observability/dcgm-exporter`, which bundled an Ubuntu userland and the
`dcgm-exporter` binary that the agent never used.

If you need to pin the DaemonSet back to a standalone DCGM image, set
`dcgmAgent.image.tag` (resolved against `eks/observability/dcgm-exporter`) or
`dcgmAgent.image.override` for a full image reference. Note that the
`dcgm-exporter` images carry the CVEs of their base OS and of the exporter
binary, so treat this as a temporary break-glass measure.

## Configuration

The following table lists the configurable parameters for this chart and their default values.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| dcgmAgent.affinity | object | see [`values.yaml`](./values.yaml) | Map of dcgm pod affinities |
| dcgmAgent.image.account | string | `"602401143452"` | ECR repository account number for the dcgm-exporter. Only used when a tag is set. |
| dcgmAgent.image.containerRegistry | string | `""` | Full container registry URL override (e.g., 602401143452.dkr.ecr.us-west-2.amazonaws.com). When set, this takes precedence over account/endpoint/region/domain fields. Only used when a tag is set. |
| dcgmAgent.image.domain | string | `"amazonaws.com"` | ECR repository domain for the dcgm-exporter. Only used when a tag is set. |
| dcgmAgent.image.endpoint | string | `"ecr"` | ECR repository endpoint for the dcgm-exporter. Only used when a tag is set. |
| dcgmAgent.image.override | string | `""` | Full image override (registry/repository:tag) for the dcgm-server DaemonSet. Takes precedence over all other fields. |
| dcgmAgent.image.pullPolicy | string | `"IfNotPresent"` | Container pull policy for the dcgm-server DaemonSet |
| dcgmAgent.image.region | string | `"us-west-2"` | ECR repository region for the dcgm-exporter. Only used when a tag is set. |
| dcgmAgent.image.tag | string | `""` | Image tag that pins the dcgm-server DaemonSet back to a standalone eks/observability/dcgm-exporter image. Empty by default, so the DaemonSet runs the nv-hostengine bundled in the eks-node-monitoring-agent image. See the "DCGM host engine" section of the chart README before setting this. |
| dcgmAgent.podAnnotations | object | `{}` | Pod annotations applied to the dcgm exporter |
| dcgmAgent.podLabels | object | `{}` | Pod labels applied to the dcgm exporter |
| dcgmAgent.resizePolicy | list | `[]` | Container resize policy for in-place pod vertical scaling (requires Kubernetes 1.33+) |
| dcgmAgent.resources | object | `{}` | Container resources for the dcgm deployment |
| dcgmAgent.tolerations | list | `[]` | Deployment tolerations for the dcgm |
| extraObjects | list | see [`values.yaml`](./values.yaml), so template expressions (e.g. {{ .Release.Namespace }}) inside the manifests are evaluated. Example:   extraObjects:     - apiVersion: monitoring.coreos.com/v1       kind: PodMonitor       metadata:         name: eks-node-monitoring-agent         namespace: {{ .Release.Namespace }}       spec:         selector:           matchLabels:             app.kubernetes.io/name: eks-node-monitoring-agent         podMetricsEndpoints:           - port: metrics |
| fullnameOverride | string | `"eks-node-monitoring-agent"` | A fullname override for the chart |
| global | object | `{"podAnnotations":{},"podLabels":{}}` | Global values shared across components |
| global.podAnnotations | object | `{}` | Annotations applied to eks-node-monitoring-agent and dcgm-exporter (can be overridden by component-specific annotations) |
| global.podLabels | object | `{}` | Labels applied to eks-node-monitoring-agent and dcgm-exporter (can be overridden by component-specific labels) |
| imagePullSecrets | list | `[]` | Docker registry pull secrets |
| nameOverride | string | `"eks-node-monitoring-agent"` | A name override for the chart |
| nodeAgent.additionalArgs | list | `["--metrics-address=:8003"]` | List of additional container arguments for the eks-node-monitoring-agent |
| nodeAgent.affinity | object | see [`values.yaml`](./values.yaml) | Map of pod affinities for the eks-node-monitoring-agent |
| nodeAgent.image.account | string | `"602401143452"` | ECR repository account number for the eks-node-monitoring-agent |
| nodeAgent.image.containerRegistry | string | `""` | Full container registry URL override (e.g., 602401143452.dkr.ecr.us-west-2.amazonaws.com). When set, this takes precedence over account/endpoint/region/domain fields. |
| nodeAgent.image.domain | string | `"amazonaws.com"` | ECR repository domain for the eks-node-monitoring-agent |
| nodeAgent.image.endpoint | string | `"ecr"` | ECR repository endpoint for the eks-node-monitoring-agent |
| nodeAgent.image.pullPolicy | string | `"IfNotPresent"` | Container pull policyfor the eks-node-monitoring-agent |
| nodeAgent.image.region | string | `"us-west-2"` | ECR repository region for the eks-node-monitoring-agent |
| nodeAgent.image.tag | string | `"v1.6.7-eksbuild.1"` | Image tag for the eks-node-monitoring-agent |
| nodeAgent.monitors | object | `{}` | Per-monitor configuration keyed by plugin name. See the main README for details. |
| nodeAgent.podAnnotations | object | `{}` | Pod annotations applied to the eks-node-monitoring-agent |
| nodeAgent.podLabels | object | `{}` | Pod labels applied to the eks-node-monitoring-agent |
| nodeAgent.probePort | int | `8002` | Health probe port for the eks-node-monitoring-agent. Used for both the --probe-address arg and the liveness probe. |
| nodeAgent.resizePolicy | list | `[]` | Container resize policy for in-place pod vertical scaling (requires Kubernetes 1.33+) |
| nodeAgent.resources | object | `{"limits":{"cpu":"250m","memory":"200Mi"},"requests":{"cpu":"10m","memory":"30Mi"}}` | Container resources for the eks-node-monitoring-agent |
| nodeAgent.securityContext | object | `{"capabilities":{"add":["NET_ADMIN"]},"privileged":true}` | Container Security context for the eks-node-monitoring-agent |
| nodeAgent.tolerations | list | `[{"operator":"Exists"}]` | Deployment tolerations for the eks-node-monitoring-agent |
| serviceAccount.annotations | object | `{}` | Annotations applied to the service account |
| serviceAccount.create | bool | `true` | Specifies whether a service account should be created |
| serviceAccount.name | string | `nil` | The name of the service account to use. If not set and create is true, a name is generated using the fullname template |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":"10%"},"type":"RollingUpdate"}` | Update strategy for all daemon sets |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install` or provide a YAML file
containing the values for the above parameters.
