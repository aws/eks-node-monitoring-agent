package storage

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register storage monitor plugin on package import
	plugin := framework.NewPlugin("storage-monitor", []monitor.Monitor{
		NewStorageMonitor(),
	}).WithNodeCondition(conditions.StorageReady, conditions.NodeConditionConfig{
		ReadyReason:  "DiskIsReady",
		ReadyMessage: "Monitoring for the Disk system is active",
	})
	registry.MustRegister(plugin)
}
