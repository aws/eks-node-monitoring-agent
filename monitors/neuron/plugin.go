package neuron

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin returns the neuron monitor plugin configured with the provided settings.
func NewPlugin(settings config.NeuronMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("neuron", []monitor.Monitor{
		&neuronMonitor{},
	})
}
