package nitrosandbox

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

func init() {
	// Register only on hosts whose hardware can run isolated pods. Enclave
	// support is fixed when the instance launches, so one check is enough, and
	// skipping registration keeps every caller that enumerates the registry from
	// naming this monitor. The check itself stays silent: a node that cannot run
	// isolated pods logs nothing about this monitor at any level.
	if !hardwareSupportsSandboxes() {
		return
	}

	registry.MustRegister(framework.NewPlugin("nitro-sandbox", []monitor.Monitor{
		NewNitroSandboxMonitor(),
	}))
}
