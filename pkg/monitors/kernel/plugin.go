package kernel

import (
	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/framework"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register kernel monitor plugin on package import
	plugin := framework.NewPlugin("kernel-monitor", []monitor.Monitor{
		&KernelMonitor{},
	})
	registry.MustRegister(plugin)
}
