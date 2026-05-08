package nvidia

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin returns the nvidia monitor plugin configured with the provided settings.
func NewPlugin(settings config.NvidiaMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("nvidia", []monitor.Monitor{
		NewNvidiaMonitor(),
	})
}
