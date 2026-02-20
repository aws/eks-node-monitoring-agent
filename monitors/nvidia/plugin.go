package nvidia

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	plugin := framework.NewPlugin("nvidia", []monitor.Monitor{
		NewNvidiaMonitor(),
	})
	if err := registry.ValidateAndRegister(plugin); err != nil {
		panic(err)
	}
}
