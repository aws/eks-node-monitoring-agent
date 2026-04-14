package networking

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin creates a networking monitor plugin with the provided settings.
// It follows the same explicit-registration pattern as the runtime monitor,
// allowing per-monitor config to be passed at construction time via Options
// rather than through post-construction setters.
func NewPlugin(settings config.NetworkingMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("networking", []monitor.Monitor{
		NewNetworkingMonitor(WithAllowedIPTablesChains(settings.AllowedIPTablesChains)),
	})
}
