package nvidia

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	plugin := framework.NewPlugin("nvidia", []monitor.Monitor{
		NewNvidiaMonitor(),
	}).WithNodeCondition(conditions.AcceleratedHardwareReady, conditions.NodeConditionConfig{
		ReadyReason:  "NvidiaGPUIsReady",
		ReadyMessage: "Monitoring for the Nvidia GPU system is active",
	}).WithRequiredHardware(config.AcceleratedHardwareNvidia)
	if err := registry.ValidateAndRegister(plugin); err != nil {
		panic(err)
	}
}
