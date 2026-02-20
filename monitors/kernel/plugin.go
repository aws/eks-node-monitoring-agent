package kernel

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Auto-register kernel monitor plugin on package import
	plugin := framework.NewPlugin("kernel-monitor", []monitor.Monitor{
		&KernelMonitor{},
	})
	registry.MustRegister(plugin)
}
