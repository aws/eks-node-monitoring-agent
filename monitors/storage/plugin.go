package storage

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin returns the storage monitor plugin configured with the provided settings.
func NewPlugin(settings config.StorageMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("storage-monitor", []monitor.Monitor{
		NewStorageMonitor(),
	})
}
