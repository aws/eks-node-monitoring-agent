package telemetry

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/scrape"
)

const NMAScope = "node-monitoring-agent"

type FatalSource interface {
	ActiveFatalConditions() []manager.ActiveFatalCondition
}

type InProcessCollector struct {
	source    FatalSource
	resources map[string]string
}

func NewInProcessCollector(source FatalSource, resources map[string]string) *InProcessCollector {
	return &InProcessCollector{source: source, resources: resources}
}

func (c *InProcessCollector) Scope() string { return NMAScope }

func (c *InProcessCollector) Scrape(context.Context) scrape.Result {
	res := scrape.Result{Scope: nodetelemetry.Scope{Name: NMAScope}}
	// TODO: send Warning conditions too.
	for _, f := range c.source.ActiveFatalConditions() {
		// TODO: check type and reason against the problem catalog, so
		// reasons such as %sRepeatedRestart can't carry arbitrary names.
		res.Problems = append(res.Problems, nodetelemetry.Problem{
			Type:      string(f.ConditionType),
			Reason:    f.Reason,
			Resources: c.resources,
		})
	}
	// TODO: add NMA's own metrics from its Prometheus registry.
	return res
}
