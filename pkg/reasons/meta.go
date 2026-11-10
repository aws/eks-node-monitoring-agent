//go:generate go run ../../tools/codegen-reasons/... --config-path reasons.yaml --template-path reasons.go.tpl --out-path reasons.go

package reasons

import (
	"fmt"
	"sort"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

type ReasonMeta struct {
	template        string
	defaultSeverity monitor.Severity
	component       string
}

// ByName returns the ReasonMeta registered under the given identifier from
// reasons.yaml, and whether such a reason exists. It allows configuration
// that references reasons by name (e.g. probe specs) to be validated against
// the registered taxonomy at startup.
//
// Note that emitted condition reasons are rendered templates: for
// parameterized reasons (e.g. "NvidiaXID%dError") the rendered string never
// matches a registered identifier, so callers resolving metadata from an
// emitted reason must handle a miss.
func ByName(name string) (ReasonMeta, bool) {
	m, ok := byName[name]
	return m, ok
}

// Components returns the distinct component names declared in reasons.yaml,
// sorted. A component is the owning agent or monitor of a set of reasons and
// is the unit of the per-component event budget.
func Components() []string {
	out := make([]string, 0, len(components))
	for c := range components {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// IsComponent reports whether name is a component declared in reasons.yaml.
func IsComponent(name string) bool {
	_, ok := components[name]
	return ok
}

// ValidateMonitorComponents checks that every given monitor name is a
// declared component in reasons.yaml. Monitor names are the fallback budget
// key for reasons that cannot be resolved through ByName (parameterized
// templates render to unregistered strings), so an undeclared monitor would
// emit outside the capacity ledger and break the derived node-ceiling
// arithmetic. Call it at startup and fail loudly on error.
func ValidateMonitorComponents(names ...string) error {
	var unknown []string
	for _, n := range names {
		if !IsComponent(n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("monitors %v are not declared components in pkg/reasons/reasons.yaml; add Component entries for their reasons (see .kiro/specs/probe-framework-event-limits-spec.md section 3.2)", unknown)
	}
	return nil
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

// Component returns the owning component of the reason as declared in
// reasons.yaml — the unit of the per-component event budget.
func (r ReasonMeta) Component() string {
	return r.component
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
