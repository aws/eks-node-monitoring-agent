[#node-health-issues]
== Node health issues

The following tables describe node health issues that can be detected by the node monitoring agent. There are two types of issues:

* Condition – A terminal issue that warrants a remediation action like an instance replacement or reboot. When auto repair is enabled, Amazon EKS will do a repair action, either as a node replacement or reboot. For more information, see <<status-node-conditions>>.

* Event – A temporary issue or sub-optimal node configuration. No auto repair action will take place. For more information, see <<status-node-events>>.

{{- range $condition, $reasons := . }}
{{ $shortCondition := trimSuffix $condition "Ready" }}
[#node-health-{{$shortCondition}}]
=== {{$shortCondition}} node health issues

The monitoring condition is `{{$condition}}` for issues in the following table that have a severity of "Condition".
{{ if eq $condition "AcceleratedHardwareReady" }}
If auto repair is enabled, the repair actions that are listed start 10 minutes after the issue is detected. For more information on XID errors, see link:https://docs.nvidia.com/deploy/xid-errors/index.html#topic_5_1[Xid Errors] in the _NVIDIA GPU Deployment and Management Documentation_. For more information on the individual XID messages, see link:https://docs.nvidia.com/deploy/gpu-debug-guidelines/index.html#understanding-xid-messages[Understanding Xid Messages] in the _NVIDIA GPU Deployment and Management Documentation_.
{{ end }}
[%header,cols="3"]
|===

|Name
|Severity
|Description
{{- range $reason, $meta := $reasons }}

|{{ sanitizeTemplate $meta.Template }}
|{{ convertSeverity $meta.DefaultSeverity }}
|{{$meta.Description}}
{{- end }}

|===
{{- end }}
