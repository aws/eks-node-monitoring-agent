package instancestore

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	registry.MustRegister(framework.NewPlugin("instance-store", []monitor.Monitor{
		&InstanceStoreMonitor{},
	}))
}
