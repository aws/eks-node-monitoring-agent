package neuron

import (
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/framework"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register neuron monitor plugin on package import
	plugin := framework.NewPlugin("neuron", []monitor.Monitor{
		&neuronMonitor{},
	})
	registry.MustRegister(plugin)
}
