package networking

import (
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/framework"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	plugin := framework.NewPlugin("networking", []monitor.Monitor{
		NewNetworkingMonitor(),
	})
	if err := registry.ValidateAndRegister(plugin); err != nil {
		panic(err)
	}
}
