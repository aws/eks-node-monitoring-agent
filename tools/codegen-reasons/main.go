package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"sort"
	"text/template"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

// maxComponents is N, the declared platform capacity for event-budget
// components. The unit of the per-component event quota is the component (an
// owning agent or monitor), and the node-global event ceiling is derived as
// N x Q, so registering capacity is a deliberate ledger change here — never
// a runtime discovery. See .kiro/specs/probe-framework-event-limits-spec.md
// section 2 and .kiro/specs/probe-framework-design.md "Limits and Scaling".
const maxComponents = 10

// reservedComponents are budget slots claimed by the platform for emitters
// that own no reasons.yaml entries (the merged diagnostics collector).
var reservedComponents = []string{"diagnostics"}

// templateData is the root object rendered into reasons.go.
type templateData struct {
	Conditions map[string]map[string]internalReasonMeta
	Components []string
}

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

	componentSet := map[string]struct{}{}
	for _, c := range reasonConfig {
		for name, r := range c {
			r.validate(name)
			componentSet[r.Component] = struct{}{}
		}
	}
	components := make([]string, 0, len(componentSet))
	for c := range componentSet {
		components = append(components, c)
	}
	sort.Strings(components)
	if len(components)+len(reservedComponents) > maxComponents {
		panic(fmt.Errorf(
			"reasons.yaml declares %d distinct components %v plus %d reserved %v, exceeding the declared platform capacity N=%d; "+
				"raising N is a deliberate ledger change that recomputes the node-global event ceiling — "+
				"see .kiro/specs/probe-framework-event-limits-spec.md section 2",
			len(components), components, len(reservedComponents), reservedComponents, maxComponents))
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
	err = template.Execute(&buf, templateData{Conditions: reasonConfig, Components: components})
	if err != nil {
		panic(err)
	}

	// The template is not written in gofmt style; format here so the
	// committed artifact cannot drift from the generator's output.
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		panic(fmt.Errorf("codegen-reasons produced invalid Go: %w", err))
	}

	err = os.WriteFile(*outPath, formatted, 0644)
	if err != nil {
		panic(err)
	}
}

type internalReasonMeta struct {
	Template        string           `yaml:"Template"`
	DefaultSeverity monitor.Severity `yaml:"DefaultSeverity"`
	Component       string           `yaml:"Component"`
}

func (ir *internalReasonMeta) validate(name string) {
	switch ir.DefaultSeverity {
	case monitor.SeverityFatal, monitor.SeverityWarning, monitor.SeverityInfo:
	default:
		panic(fmt.Errorf("severity it not an accepted value: %q", ir.DefaultSeverity))
	}
	if ir.Component == "" {
		panic(fmt.Errorf("reason %q is missing a Component: every reason must declare its owning component for the per-component event budget", name))
	}
}
