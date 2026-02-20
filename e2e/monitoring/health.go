package monitoring

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

var (
	ignoredConditionMatchers = []func(corev1.NodeCondition, appsv1.DaemonSet) bool{}
	ignoredEventMatchers     = []func(corev1.Event, appsv1.DaemonSet) bool{
		// this is an iptables rules which shows up in IPv6 cluster nodes.
		func(e corev1.Event, _ appsv1.DaemonSet) bool {
			return strings.Contains(e.Message, "Block Node Local Pod access via IPv4")
		},
		// TODO: this is a temporary fix for the nvidia-persistenced daemon that
		// seems to restart several times before becoming stable.
		func(e corev1.Event, _ appsv1.DaemonSet) bool {
			return strings.Contains(e.Message, "Nvidia-PersistencedRepeatedRestart")
		},
		// AL2 AMIs run into this event for kernel workers frequently, but it is
		// normally due to the initial load on EBS to load initial binaries.
		func(e corev1.Event, _ appsv1.DaemonSet) bool {
			return strings.Contains(e.Message, "IODelays: Process (kworker")
		},
		// Health Code 12 indicates clocks are being optimized to meet power
		// requirements. we request the maximum allowed clock rates on AL2023,
		// so this error indicates difficulty maintaining that clock rate.
		// ref: https://github.com/awslabs/amazon-eks-ami/blob/77a969c3788caa6562bda1ca30321f9d5af798a4/templates/shared/runtime/bin/set-nvidia-clocks#L25-L30
		// see: https://github.com/NVIDIA/DCGM/blob/6e947dcac9b3160d61d98fea4741d51d4bec5c1f/dcgmlib/dcgm_errors.h#L47-L48
		func(e corev1.Event, _ appsv1.DaemonSet) bool {
			return strings.Contains(e.Message, "DCGMHealthCode12")
		},
		// fabric manager only succeeds on nvswitch-based systems. this means
		// that p3/4/5/6/etc are the only instances which will behave silently.
		func(e corev1.Event, _ appsv1.DaemonSet) bool {
			return strings.Contains(e.Message, "ServiceFailedToStart: Failed to start nvidia-fabricmanager.service")
		},
	}
)

func NewHealthChecker(cfg *envconf.Config, ds *appsv1.DaemonSet) *healthChecker {
	return &healthChecker{
		kubeClient: cfg.Client(),
		daemonset:  ds,
	}
}

type healthChecker struct {
	kubeClient klient.Client
	daemonset  *appsv1.DaemonSet
}

func (c *healthChecker) HealthCheck(ctx context.Context) error {
	if err := c.waitReady(ctx); err != nil {
		return err
	}
	if err := c.checkNodes(ctx); err != nil {
		return err
	}
	if err := c.checkPods(ctx); err != nil {
		return err
	}
	if err := c.checkEvents(ctx); err != nil {
		return err
	}
	return nil
}

func (c *healthChecker) waitReady(ctx context.Context) error {
	if err := wait.For(
		conditions.New(c.kubeClient.Resources()).DaemonSetReady(c.daemonset),
		wait.WithTimeout(time.Minute),
		wait.WithContext(ctx),
	); err != nil {
		return err
	}

	if c.daemonset.Status.DesiredNumberScheduled == 0 {
		return fmt.Errorf("daemonset should be scheduled to at least one node: %+v", c.daemonset)
	}

	return nil
}

func (c *healthChecker) checkNodes(ctx context.Context) error {
	var nodeList corev1.NodeList
	if err := c.kubeClient.Resources().List(ctx, &nodeList); err != nil {
		return err
	}

	for _, node := range nodeList.Items {
		for _, nodeCondition := range node.Status.Conditions {
			if strings.HasSuffix(string(nodeCondition.Type), "Ready") && nodeCondition.Status != corev1.ConditionTrue {
				ignore := false
				for _, shouldIgnore := range ignoredConditionMatchers {
					if shouldIgnore(nodeCondition, *c.daemonset) {
						ignore = true
						break
					}
				}
				if !ignore {
					return fmt.Errorf("node condition %+v is not ready for node %s", nodeCondition, node.Name)
				}
			}
		}
	}
	return nil
}

func (c *healthChecker) checkEvents(ctx context.Context) error {
	var eventList corev1.EventList
	if err := c.kubeClient.Resources().List(ctx, &eventList); err != nil {
		return err
	}

	for _, event := range eventList.Items {
		if event.Source.Component == "eks-node-monitoring-agent" && event.LastTimestamp.After(c.daemonset.CreationTimestamp.Time) {
			ignore := false
			for _, shouldIgnore := range ignoredEventMatchers {
				if shouldIgnore(event, *c.daemonset) {
					ignore = true
					break
				}
			}
			if !ignore {
				return fmt.Errorf("should not have reported event: %v", event)
			}
		}
	}
	return nil
}

func (c *healthChecker) checkPods(ctx context.Context) error {
	selector := labels.FormatLabels(c.daemonset.Spec.Selector.MatchLabels)

	var pods corev1.PodList
	if err := c.kubeClient.Resources().List(ctx, &pods, resources.WithLabelSelector(selector)); err != nil {
		return err
	}

	for _, pod := range pods.Items {
		for _, containerStatuses := range [][]corev1.ContainerStatus{
			pod.Status.ContainerStatuses,
			pod.Status.InitContainerStatuses,
		} {
			for _, containerStatus := range containerStatuses {
				if containerStatus.RestartCount > 0 {
					return fmt.Errorf("container %s restarted %d times", containerStatus.Name, containerStatus.RestartCount)
				}
			}
		}
	}
	return nil
}
