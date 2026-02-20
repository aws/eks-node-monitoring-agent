package reasons

var (
{{- range $condition, $reasons := . }}

    // reasons for the {{$condition}} condition.
{{ range $reasonName, $reason := $reasons }}
    {{$reasonName}} = ReasonMeta{
        template:        "{{$reason.Template}}",
        defaultSeverity: "{{$reason.DefaultSeverity}}",
    }
{{- end -}}
{{- end -}}
)
