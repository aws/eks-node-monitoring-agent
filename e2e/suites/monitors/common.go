package monitors

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/eks-node-monitoring-agent/e2e/metrics"
	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/validation"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	e2ewait "sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

var hostRootVolume = corev1.Volume{
	Name: "host-root",
	VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: "/"},
	},
}

var hostRootMount = corev1.VolumeMount{
	Name:      hostRootVolume.Name,
	MountPath: "/host",
}

var privilegedContext = corev1.SecurityContext{
	RunAsUser:  aws.Int64(0),
	Privileged: aws.Bool(true),
}

// this detection jitter is added because it seems that sometimes the detections
// occurs in either the same or slightly offset timestamp due to minor clock
// skew between the test host and the node.
const detectionJitter = time.Second

func foundEvent(event *corev1.Event, startTime time.Time, condition, reason string) bool {
	return !startTime.Add(-detectionJitter).After(event.LastTimestamp.Time) && event.Reason == condition && strings.HasPrefix(event.Message, reason+":")
}

func foundCondition(nodeCondition corev1.NodeCondition, startTime time.Time, conditionType corev1.NodeConditionType, reason string) bool {
	return !startTime.Add(-detectionJitter).After(nodeCondition.LastTransitionTime.Time) && nodeCondition.Type == conditionType && nodeCondition.Reason == reason
}

func GetNodeStatusCondition(node *corev1.Node, matcherFn func(corev1.NodeCondition) bool) *corev1.NodeCondition {
	var condition *corev1.NodeCondition
	for _, c := range node.Status.Conditions {
		if matcherFn(c) {
			condition = &c
			break
		}
	}
	return condition
}

func acceleratedHardwareHealthy(node *corev1.Node, vendor string) bool {
	if node.DeletionTimestamp != nil {
		return false
	}
	condition := GetNodeStatusCondition(node, func(nc corev1.NodeCondition) bool {
		return nc.Type == nodeconditions.AcceleratedHardwareReady
	})
	return condition != nil &&
		condition.Status == corev1.ConditionTrue &&
		strings.Contains(condition.Message, vendor)
}

// replaceAcceleratedNode removes node and returns its healthy replacement.
//
// Fault injection leaves AcceleratedHardwareReady False permanently: nothing
// clears it, so Auto Mode node auto-repair reaps the node ~10min later, inside
// whatever test is running by then, evicting pods with a 1s grace period and
// bypassing PDBs. Replacing it here keeps that bounded and synchronous.
func replaceAcceleratedNode(
	ctx context.Context,
	t *testing.T,
	cfg *envconf.Config,
	ec2Client *ec2.Client,
	node *corev1.Node,
	vendor string,
) *corev1.Node {
	t.Helper()
	oldNodeName := node.Name

	// Auto Mode instances are owned by an AWS-managed service principal that
	// denies customer roles ec2:TerminateInstances, so deletion has to go
	// through Karpenter's termination finalizer instead.
	switch {
	case node.Labels[computeTypeLabelKey] == computeTypeAuto:
		t.Logf("auto mode node detected; skipping direct EC2 termination and relying on karpenter finalizer on node delete")
	default:
		t.Logf("terminating node %q to mimic node repair", oldNodeName)
		instanceId, err := validation.ParseProviderID(node.Spec.ProviderID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
			InstanceIds: []string{instanceId},
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("deleting node object %q from cluster", oldNodeName)
	if err := cfg.Client().Resources().Delete(ctx, node); err != nil && !k8serrors.IsNotFound(err) {
		t.Fatal(err)
	}

	t.Logf("waiting for node object %q to be deleted from cluster", oldNodeName)
	if err := e2ewait.For(
		conditions.New(cfg.Client().Resources()).ResourceDeleted(node),
		e2ewait.WithTimeout(10*time.Minute),
		e2ewait.WithContext(ctx),
	); err != nil {
		t.Fatal(err)
	}

	t.Logf("waiting for a new %s node to join the cluster...", vendor)
	var newNode *corev1.Node
	if err := e2ewait.For(func(ctx context.Context) (bool, error) {
		var nodeList corev1.NodeList
		if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
			return false, err
		}
		for i := range nodeList.Items {
			candidate := &nodeList.Items[i]
			if candidate.Name == oldNodeName {
				continue
			}
			if acceleratedHardwareHealthy(candidate, vendor) {
				newNode = candidate
				return true, nil
			}
		}
		return false, nil
	}, e2ewait.WithTimeout(10*time.Minute), e2ewait.WithInterval(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Logf("node %q replaced by healthy node %q", oldNodeName, newNode.Name)
	return newNode
}

func nodeConditionWaiter(ctx context.Context, cond *conditions.Condition, node *corev1.Node, startTime time.Time, conditionType corev1.NodeConditionType, reason string) wait.ConditionWithContextFunc {
	return cond.ResourceMatch(
		node,
		func(o k8s.Object) bool {
			node := o.(*corev1.Node)
			condition := GetNodeStatusCondition(node, func(nc corev1.NodeCondition) bool {
				return foundCondition(nc, startTime, conditionType, reason)
			})
			if condition == nil {
				return false
			}
			maybePublishNodeConditionMetrics(ctx, startTime, condition)
			return true
		},
	)
}

func eventWaiter(ctx context.Context, cond *conditions.Condition, node *corev1.Node, startTime time.Time, conditionType corev1.NodeConditionType, reason string) wait.ConditionWithContextFunc {
	return cond.ResourceListMatchN(
		&corev1.EventList{}, 1,
		func(o k8s.Object) bool {
			e := o.(*corev1.Event)
			if !foundEvent(e, startTime, string(conditionType), reason) {
				return false
			}
			maybePublishEventMetrics(ctx, startTime, e)
			return true
		},
		resources.WithFieldSelector("involvedObject.name="+node.Name),
	)
}

func maybePublishEventMetrics(ctx context.Context, startTime time.Time, event *corev1.Event) error {
	if !metrics.MetricsEnabled {
		return nil
	}
	eventParts := strings.SplitN(event.Message, ":", 2)
	if len(eventParts) != 2 {
		return fmt.Errorf("could not split event message by delimeter(':'): %q", event.Message)
	}
	eventReason := eventParts[0]
	return metrics.PublishDetectionMetrics(ctx, metrics.DetectionData{
		// this might look confusing but our explicit mapping of conditions to
		// events requires that we use the Reason field to hold the condition.
		ConditionType: event.Reason,
		Reason:        eventReason,
		Delay:         event.LastTimestamp.Sub(startTime),
	})
}

func maybePublishNodeConditionMetrics(ctx context.Context, startTime time.Time, nodeCondition *corev1.NodeCondition) error {
	if !metrics.MetricsEnabled {
		return nil
	}
	return metrics.PublishDetectionMetrics(ctx, metrics.DetectionData{
		ConditionType: string(nodeCondition.Type),
		Reason:        nodeCondition.Reason,
		Delay:         nodeCondition.LastHeartbeatTime.Sub(startTime),
	})
}
