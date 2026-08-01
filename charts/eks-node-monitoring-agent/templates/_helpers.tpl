{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "eks-node-monitoring-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "eks-node-monitoring-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "eks-node-monitoring-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common selector labels
*/}}
{{- define "eks-node-monitoring-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "eks-node-monitoring-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "eks-node-monitoring-agent.labels" -}}
{{ include "eks-node-monitoring-agent.selectorLabels" . }}
helm.sh/chart: {{ include "eks-node-monitoring-agent.name" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: controller
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "eks-node-monitoring-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "eks-node-monitoring-agent.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Get the image that runs the bundled DCGM host engine (nv-hostengine).

The engine ships in the eks-node-monitoring-agent image itself, so by default
this resolves to the same image as the node agent. The dcgmAgent.image.* fields
remain supported as a break-glass path: setting `override`, or a `tag` for the
eks/observability/dcgm-exporter repository, pins the DaemonSet back to a
standalone DCGM image.
*/}}
{{- define "dcgm-server.image" -}}
{{- if .Values.dcgmAgent.image.override }}
{{- .Values.dcgmAgent.image.override }}
{{- else if .Values.dcgmAgent.image.tag }}
{{- if .Values.dcgmAgent.image.containerRegistry }}
{{- printf "%s/eks/observability/dcgm-exporter:%s" .Values.dcgmAgent.image.containerRegistry .Values.dcgmAgent.image.tag -}}
{{- else }}
{{- printf "%s.dkr.%s.%s.%s/eks/observability/dcgm-exporter:%s" .Values.dcgmAgent.image.account .Values.dcgmAgent.image.endpoint .Values.dcgmAgent.image.region .Values.dcgmAgent.image.domain .Values.dcgmAgent.image.tag -}}
{{- end -}}
{{- else }}
{{- include "eks-node-monitoring-agent.image" . }}
{{- end -}}
{{- end -}}

{{/*
Get the current NMA image for a region.
*/}}
{{- define "eks-node-monitoring-agent.image" -}}
{{- if .Values.nodeAgent.image.override }}
{{- .Values.nodeAgent.image.override }}
{{- else if .Values.nodeAgent.image.containerRegistry }}
{{- printf "%s/eks/eks-node-monitoring-agent:%s" .Values.nodeAgent.image.containerRegistry .Values.nodeAgent.image.tag }}
{{- else }}
{{- printf "%s.dkr.%s.%s.%s/eks/eks-node-monitoring-agent:%s" .Values.nodeAgent.image.account .Values.nodeAgent.image.endpoint .Values.nodeAgent.image.region .Values.nodeAgent.image.domain .Values.nodeAgent.image.tag }}
{{- end -}}
{{- end -}}
