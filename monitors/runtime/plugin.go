package runtime

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewPlugin returns the runtime monitor plugin configured with the provided settings.
// node and kubeClient are required to update node annotations for EKS Auto mode.
func NewPlugin(settings config.RuntimeMonitorSettings, node *corev1.Node, kubeClient client.Client) registry.MonitorPlugin {
	return framework.NewPlugin("runtime", []monitor.Monitor{
		NewRuntimeMonitor(node, kubeClient),
	})
}
