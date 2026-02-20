package monitors

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func StressFileObserver() types.Feature {
	var pod = corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "file-observer-stress",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "file-observer-stress",
					Command: []string{
						"/bin/bash",
						"-c",
						"while true; do echo 'log' | tee -a /host/dev/kmsg; sleep 0.001; done",
					},
					Image:           "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
					SecurityContext: &privilegedContext,
					VolumeMounts:    []corev1.VolumeMount{hostRootMount},
				},
			},
			Volumes: []corev1.Volume{hostRootVolume},
		},
	}

	return features.New("StressFileObserver").
		WithLabel("type", "stress").
		Assess("FileWrites", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			targetNode := &nodeList.Items[0]
			t.Logf("targetting node %q for test", targetNode.Name)
			pod.Spec.NodeName = targetNode.Name
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			const sleepTime = 5 * time.Minute
			t.Logf("running stress workload for %.2f minutes...", sleepTime.Minutes())
			time.Sleep(sleepTime)
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
