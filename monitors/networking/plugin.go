package networking

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	plugin := framework.NewPlugin("networking", []monitor.Monitor{
		NewNetworkingMonitor(),
	}).WithNodeCondition(conditions.NetworkingReady, conditions.NodeConditionConfig{
		ReadyReason:  "NetworkingIsReady",
		ReadyMessage: "Monitoring for the Networking system is active",
	})
	if err := registry.ValidateAndRegister(plugin); err != nil {
		panic(err)
	}
}
