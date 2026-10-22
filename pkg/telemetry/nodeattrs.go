package telemetry

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

const (
	labelZoneID       = "topology.k8s.aws/zone-id"
	awsProviderPrefix = "aws://"
	instanceIDPrefix  = "i-"
)

// NodeAttrsFromNode returns the report's resource attributes from the Node.
func NodeAttrsFromNode(node *corev1.Node) nodetelemetry.NodeAttrs {
	return nodetelemetry.NodeAttrs{
		NodeName:       node.Name,
		HostID:         InstanceID(node.Spec.ProviderID),
		HostArch:       node.Labels[corev1.LabelArchStable],
		HostType:       node.Labels[corev1.LabelInstanceTypeStable],
		ZoneID:         node.Labels[labelZoneID],
		KubeletVersion: node.Status.NodeInfo.KubeletVersion,
		// TODO: add host.image.id (the AMI ID) from IMDS.
	}
}

// InstanceID returns the EC2 instance ID from an aws:// provider ID, or ""
// if it isn't one.
func InstanceID(providerID string) string {
	id := providerID[strings.LastIndex(providerID, "/")+1:]
	if !strings.HasPrefix(providerID, awsProviderPrefix) || !strings.HasPrefix(id, instanceIDPrefix) {
		return ""
	}
	return id
}
