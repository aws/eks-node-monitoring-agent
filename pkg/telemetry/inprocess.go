package telemetry

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/scrape"
)

// NMAScope is the report scope for NMA's own problems.
const NMAScope = "node-monitoring-agent"

// FatalSource provides NMA's unresolved fatal conditions.
type FatalSource interface {
	ActiveFatalConditions() []manager.ActiveFatalCondition
}

// InProcessCollector reports NMA's unresolved fatal conditions as problems.
type InProcessCollector struct {
	source    FatalSource
	resources map[string]string
}

// NewInProcessCollector returns an InProcessCollector that attaches resources
// to every problem.
func NewInProcessCollector(source FatalSource, resources map[string]string) *InProcessCollector {
	return &InProcessCollector{source: source, resources: resources}
}

// Scope returns NMAScope.
func (collector *InProcessCollector) Scope() string { return NMAScope }

// Scrape returns the active fatal conditions. It never fails.
func (collector *InProcessCollector) Scrape(context.Context) scrape.Result {
	res := scrape.Result{Scope: nodetelemetry.Scope{Name: NMAScope}}
	// TODO: send Warning conditions too.
	for _, condition := range collector.source.ActiveFatalConditions() {
		// TODO: check type and reason against the problem catalog, so
		// reasons such as %sRepeatedRestart can't carry arbitrary names.
		res.Problems = append(res.Problems, nodetelemetry.Problem{
			Type:      string(condition.ConditionType),
			Reason:    condition.Reason,
			Resources: collector.resources,
		})
	}
	// TODO: add NMA's own metrics from its Prometheus registry.
	return res
}
