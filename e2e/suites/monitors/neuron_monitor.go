package monitors

import (
	"context"
	"strings"
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

func NeuronMonitor() types.Feature {
	var targetNode *corev1.Node

	var pod = corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "neuron-hw-error",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "neuron-hw-error",
					Command: []string{
						"/bin/bash",
						"-c",
						"echo 'kernel: NEURON_HW_ERR=SRAM_UNCORRECTABLE_ERROR affecting neuron devices' | tee -a /host/dev/kmsg",
					},
					Image:           "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
					SecurityContext: &privilegedContext,
					VolumeMounts:    []corev1.VolumeMount{hostRootMount},
				},
			},
			Volumes: []corev1.Volume{hostRootVolume},
		},
	}

	return features.New("NeuronMonitor").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			// search through the nodes in the cluster to see if any are neuron
			// variants. we can tell by using the nodeCondition message.
			for _, node := range nodeList.Items {
				condition := GetNodeStatusCondition(&node, func(nc corev1.NodeCondition) bool { return nc.Type == nodeconditions.AcceleratedHardwareReady })
				if condition != nil {
					if condition.Status != corev1.ConditionTrue {
						t.Fatalf("status of condition %+v was not %s", condition, corev1.ConditionTrue)
					}
					if strings.Contains(condition.Message, "Neuron") {
						targetNode = &node
						break
					}
				}
			}
			if targetNode == nil {
				t.Skipf("skipping because none of the nodes are running the neuron monitor")
			}
			t.Logf("targetting node %q for test", targetNode.Name)
			pod.Spec.NodeName = targetNode.Name
			return ctx
		}).
		Assess("NeuronSRAMUncorrectableError", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			startTime := metav1.Now()
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				nodeConditionWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, nodeconditions.AcceleratedHardwareReady, "NeuronSRAMUncorrectableError"),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
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
