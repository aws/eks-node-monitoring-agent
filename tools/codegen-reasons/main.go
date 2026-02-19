package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"text/template"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

func main() {
	reasonConfigPath := flag.String("config-path", "", "path to the config file for reasons")
	reasonTemplatePath := flag.String("template-path", "", "path to the template file for reason")
	outPath := flag.String("out-path", "", "path to output the file")
	flag.Parse()

	reasonConfigData, err := os.ReadFile(*reasonConfigPath)
	if err != nil {
		panic(err)
	}

	var reasonConfig map[string]map[string]internalReasonMeta
	err = yaml.Unmarshal(reasonConfigData, &reasonConfig)
	if err != nil {
		panic(err)
	}

	for _, c := range reasonConfig {
		for _, r := range c {
			r.validate()
		}
	}

	reasonTemplateData, err := os.ReadFile(*reasonTemplatePath)
	if err != nil {
		panic(err)
	}

	template, err := template.New("reasons").Parse(string(reasonTemplateData))
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer
	err = template.Execute(&buf, reasonConfig)
	if err != nil {
		panic(err)
	}

	err = os.WriteFile(*outPath, buf.Bytes(), 0644)
	if err != nil {
		panic(err)
	}
}

type internalReasonMeta struct {
	Template        string           `yaml:"Template"`
	DefaultSeverity monitor.Severity `yaml:"DefaultSeverity"`
}

func (ir *internalReasonMeta) validate() {
	switch ir.DefaultSeverity {
	case monitor.SeverityFatal, monitor.SeverityWarning, monitor.SeverityInfo:
	default:
		panic(fmt.Errorf("severity it not an accepted value: %q", ir.DefaultSeverity))
	}
}
