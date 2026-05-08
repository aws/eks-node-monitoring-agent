package networking

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin returns the networking plugin configured with the provided settings.
func NewPlugin(settings config.NetworkingMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("networking", []monitor.Monitor{
		NewNetworkingMonitor(WithAllowedIPTablesChains(settings.AllowedIPTablesChains)),
	})
}
