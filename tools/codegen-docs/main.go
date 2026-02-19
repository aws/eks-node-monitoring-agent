package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/template"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

//go:embed doc.adoc.tpl
var docTemplate string

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	reasonConfigPath := flag.String("config-path", "pkg/reasons/reasons.yaml", "path to the config file for reasons")
	flag.Parse()

	reasonConfigData, err := os.ReadFile(*reasonConfigPath)
	if err != nil {
		return err
	}

	var reasonConfig map[string]map[string]internalReasonMeta
	err = yaml.Unmarshal(reasonConfigData, &reasonConfig)
	if err != nil {
		return err
	}

	template := template.Must(
		template.
			New("repair-doc").
			Funcs(template.FuncMap{
				"convertSeverity":  severityObject,
				"sanitizeTemplate": sanitizeTemplate,
				"trimSuffix":       strings.TrimSuffix,
			}).
			Parse(string(docTemplate)),
	)

	return template.Execute(os.Stdout, reasonConfig)
}

type internalReasonMeta struct {
	Template        string           `yaml:"Template"`
	DefaultSeverity monitor.Severity `yaml:"DefaultSeverity"`
	Description     string           `yaml:"Description"`
}

func severityObject(sev monitor.Severity) string {
	switch sev {
	case monitor.SeverityFatal:
		return "Condition"
	case monitor.SeverityWarning, monitor.SeverityInfo:
		return "Event"
	}
	panic(fmt.Errorf("severity it not an accepted value: %q", sev))
}

func sanitizeTemplate(in string) string {
	out := in
	out = strings.ReplaceAll(out, "%d", "[Code]")
	out = strings.ReplaceAll(out, "%s", "[Name]")
	if strings.Contains(out, "%") {
		panic("string still contains template arguments: " + out)
	}
	return out
}
