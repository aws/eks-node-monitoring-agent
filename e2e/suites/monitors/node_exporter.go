package monitors

import (
	"context"
	"testing"
	"time"

	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

// ExporterImpl is a smoke test to validate at least one of each type of mode
// from the exporter. The current options are as a NodeCondition or as an Event.
func ExporterImpl() types.Feature {
	var targetNode *corev1.Node

	return features.New("ExporterImpl").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			targetNode = &nodeList.Items[0]
			t.Logf("targetting node %q for test", targetNode.Name)
			return ctx
		}).
		Assess("Event", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			conditionType, reason, pod := createEventPod(targetNode.Name)
			startTime := metav1.Now()
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				eventWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, conditionType, reason),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Errorf("failed to verify Event was present: %s", err)
			}
			if err := cfg.Client().Resources().Delete(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).ResourceDeleted(&pod),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("NodeCondition", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			conditionType, reason, pod := createNodeConditionPod(targetNode.Name)
			startTime := metav1.Now()
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				nodeConditionWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, conditionType, reason),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Errorf("failed to verify NodeCondition was present: %s", err)
			}
			if err := cfg.Client().Resources().Delete(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).ResourceDeleted(&pod),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Feature()
}

func createEventPod(nodeName string) (conditionType corev1.NodeConditionType, reason string, pod corev1.Pod) {
	return nodeconditions.KernelReady, "SoftLockup", corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name: "event",
					Command: []string{
						"/bin/bash",
						"-c",
						"echo 'watchdog: BUG: soft lockup - CPU#6 stuck for 23s! [VM Thread:4054]' | tee -a /host/dev/kmsg",
					},
					Image:           "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
					SecurityContext: &privilegedContext,
					VolumeMounts:    []corev1.VolumeMount{hostRootMount},
				},
			},
			Volumes: []corev1.Volume{hostRootVolume},
		},
	}
}

func createNodeConditionPod(nodeName string) (conditionType corev1.NodeConditionType, reason string, pod corev1.Pod) {
	return nodeconditions.NetworkingReady, "IPAMDNotReady", corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-condition",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name: "node-condition",
					Command: []string{
						"/bin/bash",
						"-c",
						"echo 'Unable to reach API Server' | tee -a /host/var/log/aws-routed-eni/ipamd.log",
					},
					Image:           "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
					SecurityContext: &privilegedContext,
					VolumeMounts:    []corev1.VolumeMount{hostRootMount},
				},
			},
			Volumes: []corev1.Volume{hostRootVolume},
		},
	}
}
