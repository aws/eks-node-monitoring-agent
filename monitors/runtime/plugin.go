package runtime

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewPlugin creates a runtime monitor plugin with the provided node and Kubernetes client.
// The runtime monitor requires a node object and client to update node annotations for
// EKS Auto mode manifest deprecation warnings.
func NewPlugin(node *corev1.Node, kubeClient client.Client) registry.MonitorPlugin {
	return framework.NewPlugin("runtime", []monitor.Monitor{
		NewRuntimeMonitor(node, kubeClient),
	})
}
