package neuron

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register neuron monitor plugin on package import
	plugin := framework.NewPlugin("neuron", []monitor.Monitor{
		&neuronMonitor{},
	}).WithNodeCondition(conditions.AcceleratedHardwareReady, conditions.NodeConditionConfig{
		ReadyReason:  "NeuronAcceleratedHardwareIsReady",
		ReadyMessage: "Monitoring for the Neuron AcceleratedHardware system is active",
	}).WithRequiredHardware(config.AcceleratedHardwareNeuron)
	registry.MustRegister(plugin)
}
