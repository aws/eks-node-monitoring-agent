package basic

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const (
	daemonSetName      = "eks-node-monitoring-agent"
	daemonSetNamespace = "kube-system"
	crdName            = "nodediagnostics.eks.amazonaws.com"
)

// DaemonSetReady verifies that the eks-node-monitoring-agent DaemonSet is ready.
func DaemonSetReady() features.Feature {
	return features.New("DaemonSetReady").
		WithLabel("suite", "basic").
		Assess("daemonset exists and is ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ds := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      daemonSetName,
					Namespace: daemonSetNamespace,
				},
			}
			err := wait.For(
				conditions.New(cfg.Client().Resources()).DaemonSetReady(ds),
				wait.WithTimeout(2*time.Minute),
			)
			if err != nil {
				t.Fatalf("DaemonSet %s/%s not ready: %v", daemonSetNamespace, daemonSetName, err)
			}
			t.Logf("DaemonSet %s/%s is ready", daemonSetNamespace, daemonSetName)
			return ctx
		}).
		Feature()
}

// PodsHealthy verifies that all pods from the DaemonSet are running and healthy.
func PodsHealthy() features.Feature {
	return features.New("PodsHealthy").
		WithLabel("suite", "basic").
		Assess("all pods are running and healthy", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var pods corev1.PodList
			err := cfg.Client().Resources(daemonSetNamespace).List(ctx, &pods,
				resources.WithLabelSelector("app.kubernetes.io/name="+daemonSetName),
			)
			if err != nil {
				t.Fatalf("failed to list pods: %v", err)
			}

			if len(pods.Items) == 0 {
				t.Fatalf("no pods found for DaemonSet %s/%s", daemonSetNamespace, daemonSetName)
			}

			for _, pod := range pods.Items {
				if pod.Status.Phase != corev1.PodRunning {
					t.Errorf("pod %s is not running: %s", pod.Name, pod.Status.Phase)
					continue
				}
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
						t.Errorf("pod %s is not ready", pod.Name)
					}
				}
				t.Logf("pod %s is running and healthy", pod.Name)
			}
			return ctx
		}).
		Feature()
}

// CRDsInstalled verifies that the NodeDiagnostic CRD is installed.
func CRDsInstalled() features.Feature {
	return features.New("CRDsInstalled").
		WithLabel("suite", "basic").
		Assess("NodeDiagnostic CRD exists", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Use unstructured client to get CRD since apiextensions types aren't in the default scheme
			crd := &unstructured.Unstructured{}
			crd.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "apiextensions.k8s.io",
				Version: "v1",
				Kind:    "CustomResourceDefinition",
			})
			err := cfg.Client().Resources().Get(ctx, crdName, "", crd)
			if err != nil {
				t.Fatalf("CRD %s not found: %v", crdName, err)
			}
			t.Logf("CRD %s is installed", crdName)
			return ctx
		}).
		Feature()
}
