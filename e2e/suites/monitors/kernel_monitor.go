package monitors

import (
	"context"
	"testing"
	"time"

	nodeconditions "golang.a2z.com/Eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func KernelMonitor() types.Feature {
	var targetNode *corev1.Node
	var pod = corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "soft-lockup",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "soft-lockup",
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

	return features.New("KernelMonitor").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			targetNode = &nodeList.Items[0]
			t.Logf("targetting node %q for test", targetNode.Name)
			pod.Spec.NodeName = targetNode.Name
			return ctx
		}).
		Assess("SoftLockup", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			startTime := metav1.Now()
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				eventWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, nodeconditions.KernelReady, "SoftLockup"),
				wait.WithTimeout(time.Minute),
			); err != nil {
				t.Fatalf("failed to verify event %q was present: %s", "SoftLockup", err)
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
