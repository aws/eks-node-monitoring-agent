package neuron

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register neuron monitor plugin on package import
	plugin := framework.NewPlugin("neuron", []monitor.Monitor{
		&neuronMonitor{},
	})
	registry.MustRegister(plugin)
}
