//go:generate go run ../../tools/codegen-reasons/... --config-path reasons.yaml --template-path reasons.go.tpl --out-path reasons.go

package reasons

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

type ReasonMeta struct {
	template        string
	defaultSeverity monitor.Severity
}

// ByName returns the ReasonMeta registered under the given identifier from
// reasons.yaml, and whether such a reason exists. It allows configuration
// that references reasons by name (e.g. probe specs) to be validated against
// the registered taxonomy at startup.
func ByName(name string) (ReasonMeta, bool) {
	m, ok := byName[name]
	return m, ok
}

// Template returns the reason template string as declared in reasons.yaml.
// Templates may contain fmt verbs (e.g. "NvidiaXID%dError") that are rendered
// by Builder.
func (r ReasonMeta) Template() string {
	return r.template
}

// DefaultSeverity returns the severity assigned to the reason in reasons.yaml.
func (r ReasonMeta) DefaultSeverity() monitor.Severity {
	return r.defaultSeverity
}

func (r ReasonMeta) Builder(templateArgs ...any) ConditionBuilder {
	return ConditionBuilder{monitor.Condition{
		Reason:   fmt.Sprintf(r.template, templateArgs...),
		Severity: r.defaultSeverity,
	}}
}

type ConditionBuilder struct {
	monitor.Condition
}

func (r ConditionBuilder) Message(msg string) ConditionBuilder {
	r.Condition.Message = msg
	return r
}

func (r ConditionBuilder) Severity(sev monitor.Severity) ConditionBuilder {
	r.Condition.Severity = sev
	return r
}

func (r ConditionBuilder) MinOccurrences(minOccurrences int64) ConditionBuilder {
	r.Condition.MinOccurrences = minOccurrences
	return r
}

func (r ConditionBuilder) Build() monitor.Condition {
	return r.Condition
}
