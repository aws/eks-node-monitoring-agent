package reasons

import (
	"fmt"

	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
)

type ReasonMeta struct {
	template        string
	defaultSeverity monitor.Severity
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
