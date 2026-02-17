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

## Configuration

The following table lists the configurable parameters for this chart and their default values.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| dcgmAgent.affinity | object | see [`values.yaml`](./values.yaml) | Map of dcgm pod affinities |
| dcgmAgent.image.account | string | `"602401143452"` | ECR repository account number for the dcgm-exporter |
| dcgmAgent.image.domain | string | `"amazonaws.com"` | ECR repository domain for the dcgm-exporter |
| dcgmAgent.image.endpoint | string | `"ecr"` | ECR repository endpoint for the dcgm-exporter |
| dcgmAgent.image.pullPolicy | string | `"IfNotPresent"` | Container pull policy for the dcgm-exporter |
| dcgmAgent.image.region | string | `"us-west-2"` | ECR repository region for the dcgm-exporter |
| dcgmAgent.image.tag | string | `"4.5.2-4.8.1-ubuntu22.04"` | Image tag for the dcgm-exporter |
| dcgmAgent.podAnnotations | object | `{}` | Pod annotations applied to the dcgm exporter |
| dcgmAgent.podLabels | object | `{}` | Pod labels applied to the dcgm exporter |
| dcgmAgent.resources | object | `{}` | Container resources for the dcgm deployment |
| dcgmAgent.tolerations | list | `[]` | Deployment tolerations for the dcgm |
| fullnameOverride | string | `"eks-node-monitoring-agent"` | A fullname override for the chart |
| global | object | `{"podAnnotations":{},"podLabels":{}}` | Global values shared across components |
| global.podAnnotations | object | `{}` | Annotations applied to eks-node-monitoring-agent and dcgm-exporter (can be overridden by component-specific annotations) |
| global.podLabels | object | `{}` | Labels applied to eks-node-monitoring-agent and dcgm-exporter (can be overridden by component-specific labels) |
| imagePullSecrets | list | `[]` | Docker registry pull secrets |
| nameOverride | string | `"eks-node-monitoring-agent"` | A name override for the chart |
| nodeAgent.additionalArgs | list | `[]` | List of addittional container arguments for the eks-node-monitoring-agent |
| nodeAgent.affinity | object | see [`values.yaml`](./values.yaml) | Map of pod affinities for the eks-node-monitoring-agent |
| nodeAgent.image.account | string | `"602401143452"` | ECR repository account number for the eks-node-monitoring-agent |
| nodeAgent.image.domain | string | `"amazonaws.com"` | ECR repository domain for the eks-node-monitoring-agent |
| nodeAgent.image.endpoint | string | `"ecr"` | ECR repository endpoint for the eks-node-monitoring-agent |
| nodeAgent.image.pullPolicy | string | `"IfNotPresent"` | Container pull policyfor the eks-node-monitoring-agent |
| nodeAgent.image.region | string | `"us-west-2"` | ECR repository region for the eks-node-monitoring-agent |
| nodeAgent.image.tag | string | `"v1.5.1-eksbuild.1"` | Image tag for the eks-node-monitoring-agent |
| nodeAgent.podAnnotations | object | `{}` | Pod annotations applied to the eks-node-monitoring-agent |
| nodeAgent.podLabels | object | `{}` | Pod labels applied to the eks-node-monitoring-agent |
| nodeAgent.resources | object | `{"limits":{"cpu":"250m","memory":"200Mi"},"requests":{"cpu":"10m","memory":"30Mi"}}` | Container resources for the eks-node-monitoring-agent |
| nodeAgent.securityContext | object | `{"capabilities":{"add":["NET_ADMIN"]},"privileged":true}` | Container Security context for the eks-node-monitoring-agent |
| nodeAgent.tolerations | list | `[{"operator":"Exists"}]` | Deployment tolerations for the eks-node-monitoring-agent |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":"10%"},"type":"RollingUpdate"}` | Update strategy for all daemon sets |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install` or provide a YAML file
containing the values for the above parameters.
