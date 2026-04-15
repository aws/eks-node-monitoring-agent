package kernel

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// NewPlugin returns the kernel monitor plugin configured with the provided settings.
func NewPlugin(settings config.KernelMonitorSettings) registry.MonitorPlugin {
	return framework.NewPlugin("kernel-monitor", []monitor.Monitor{
		&KernelMonitor{},
	})
}
