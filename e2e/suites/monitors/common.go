package monitors

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/metrics"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
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
