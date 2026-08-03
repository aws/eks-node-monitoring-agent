package nodediagnostic

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	npServerDeploy   = "np-server"
	npServerReplicas = 2
	npPodLabelKey    = "app"
	npPodLabelVal    = "np-server"
	awsNodeNamespace = "kube-system"
	awsNodeDSName    = "aws-node"
	nodeAgentName    = "aws-eks-nodeagent"
	// Pinned tag, not :latest, for reproducibility.
	npServerImage = "public.ecr.aws/nginx/nginx:1.27"
)

// EbpfMapCollectionWithNetworkPolicy applies a default-deny NetworkPolicy and a
// matching workload so the network policy agent programs eBPF maps, then asserts
// the map dump is populated in bundles captured from the node(s) hosting the
// selected pods. It skips (rather than fails) when preconditions this repo can't
// observe are unmet: no bucket flag, no nodeagent container, or no node produced
// a dump (enforcement disabled).
func EbpfMapCollectionWithNetworkPolicy(awsConfig aws.Config) types.Feature {
	nodeDiagnostics := map[string]v1alpha1.NodeDiagnostic{}
	testTimestamp := time.Now().Format("2006-01-02.150405")

	s3Client := s3.NewFromConfig(awsConfig)
	presignClient := s3.NewPresignClient(s3Client, func(po *s3.PresignOptions) {
		po.Expires = 30 * time.Minute
	})

	// Random namespace so concurrent runs against one cluster don't collide.
	nsName := envconf.RandomName("ebpf-np-e2e", 16)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	labels := map[string]string{npPodLabelKey: npPodLabelVal}
	deployment := npServerDeployment(nsName, labels)
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-ingress", Namespace: nsName},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: labels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}

	return features.New("EbpfMapCollectionWithNetworkPolicy").
		WithLabel("type", "log-collection").
		WithSetup("RequireNetworkPolicyAgent", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if nodeDiagnosticLogBucket == "" {
				t.Skip("skipping: --nodediagnostic-bucket-name flag was not provided")
			}
			if !networkPolicyAgentPresent(ctx, t, cfg) {
				t.Skipf("skipping: %s container not found on %s/%s daemonset; network policy agent is not installed",
					nodeAgentName, awsNodeNamespace, awsNodeDSName)
			}
			return ctx
		}).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}
			if err := v1alpha1.SchemeBuilder.AddToScheme(client.Resources().GetScheme()); err != nil {
				t.Fatal(err)
			}
			// Setup failures don't run the feature Teardown, so clean up
			// explicitly to avoid leaking a namespace into the next run.
			created := []k8s.Object{}
			cleanup := func() {
				for i := len(created) - 1; i >= 0; i-- {
					_ = client.Resources().Delete(ctx, created[i])
				}
				waitForNamespaceGone(ctx, client, namespace)
			}
			for _, obj := range []k8s.Object{namespace, deployment, policy} {
				if err := client.Resources().Create(ctx, obj); err != nil {
					cleanup()
					t.Fatalf("failed to create %T: %s", obj, err)
				}
				created = append(created, obj)
			}
			if err := wait.For(
				conditions.New(client.Resources()).ResourceMatch(deployment, func(object k8s.Object) bool {
					d := object.(*appsv1.Deployment)
					return d.Status.ReadyReplicas == int32(npServerReplicas)
				}),
				wait.WithTimeout(3*time.Minute),
				wait.WithContext(ctx),
			); err != nil {
				cleanup()
				t.Fatalf("np-server deployment did not become ready: %s", err)
			}

			// Only nodes hosting selected pods can have policy maps programmed.
			targetNodes := nodesRunningSelectedPods(ctx, t, client, nsName)
			if len(targetNodes) == 0 {
				cleanup()
				t.Fatal("no np-server pods were scheduled to any node")
			}

			waitForSelectedPodsRunning(ctx, client, nsName)

			for node := range targetNodes {
				nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-ebpf-%s.tgz", testTimestamp, node))
				presignedRequest, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
					Bucket: &nodeDiagnosticLogBucket,
					Key:    &nodeDiagnosticLogKey,
				})
				if err != nil {
					cleanup()
					t.Fatalf("failed to create presigned s3 PUT: %s", err)
				}
				nodeDiagnostic := v1alpha1.NodeDiagnostic{
					ObjectMeta: metav1.ObjectMeta{Name: node},
					Spec: v1alpha1.NodeDiagnosticSpec{
						LogCapture: &v1alpha1.LogCapture{
							UploadDestination: v1alpha1.UploadDestination(presignedRequest.URL),
						},
					},
				}
				if err := client.Resources().Create(ctx, &nodeDiagnostic); err != nil {
					cleanup()
					t.Fatal(err)
				}
				nodeDiagnostics[node] = nodeDiagnostic
			}
			return ctx
		}).
		Assess("MapDumpPopulated", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			nodesWithMaps := 0
			for node := range nodeDiagnostics {
				nodeDiagnostic := nodeDiagnostics[node]
				if err := cfg.Client().Resources().Get(ctx, nodeDiagnostic.Name, nodeDiagnostic.Namespace, &nodeDiagnostic); err != nil {
					t.Fatal(err)
				}
				if err := wait.For(
					conditions.New(cfg.Client().Resources()).ResourceMatch(&nodeDiagnostic, func(object k8s.Object) bool {
						nd := object.(*v1alpha1.NodeDiagnostic)
						return len(nd.Status.CaptureStatuses) > 0 && nd.Status.CaptureStatuses[0].State.Completed != nil
					}),
					wait.WithTimeout(time.Minute),
					wait.WithContext(ctx),
				); err != nil {
					t.Fatal(err)
				}

				nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-ebpf-%s.tgz", testTimestamp, node))
				objectResponse, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
					Bucket: &nodeDiagnosticLogBucket,
					Key:    &nodeDiagnosticLogKey,
				})
				if err != nil {
					t.Fatal(err)
				}
				if mapDumpPopulated(t, node, objectResponse.Body) {
					nodesWithMaps++
				}
			}
			// No dump on any target node means enforcement is disabled (agent
			// present but off) — a valid config, so skip rather than fail.
			if nodesWithMaps == 0 {
				t.Skip("no target node produced an eBPF map dump; network policy enforcement appears disabled on this cluster")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			for node := range nodeDiagnostics {
				nd := nodeDiagnostics[node]
				_ = client.Resources().Delete(ctx, &nd)
			}
			_ = client.Resources().Delete(ctx, namespace)
			waitForNamespaceGone(ctx, client, namespace)
			return ctx
		}).
		Feature()
}

func npServerDeployment(namespace string, labels map[string]string) *appsv1.Deployment {
	replicas := int32(npServerReplicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: npServerDeploy, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "server",
						Image: npServerImage,
						Ports: []corev1.ContainerPort{{ContainerPort: 80, Protocol: corev1.ProtocolTCP}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(80)},
							},
						},
					}},
				},
			},
		},
	}
}

// networkPolicyAgentPresent reports whether the aws-node daemonset runs the
// aws-eks-nodeagent container (the network policy agent).
func networkPolicyAgentPresent(ctx context.Context, t *testing.T, cfg *envconf.Config) bool {
	var ds appsv1.DaemonSet
	if err := cfg.Client().Resources().Get(ctx, awsNodeDSName, awsNodeNamespace, &ds); err != nil {
		t.Logf("could not get %s/%s daemonset: %s", awsNodeNamespace, awsNodeDSName, err)
		return false
	}
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == nodeAgentName {
			return true
		}
	}
	return false
}

// nodesRunningSelectedPods returns the set of node names hosting np-server pods.
func nodesRunningSelectedPods(ctx context.Context, t *testing.T, client klient.Client, namespace string) map[string]struct{} {
	var pods corev1.PodList
	if err := client.Resources(namespace).List(ctx, &pods,
		resources.WithLabelSelector(npPodLabelKey+"="+npPodLabelVal)); err != nil {
		t.Fatalf("failed to list np-server pods: %s", err)
	}
	nodes := map[string]struct{}{}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = struct{}{}
		}
	}
	return nodes
}

// waitForSelectedPodsRunning bounds the pre-capture wait on pod readiness. It
// does not sleep on the agent's reconcile latency: mapDumpPopulated decides
// skip-vs-fail from the recorded selection line, so a slow agent surfaces as an
// observed state, not a timing race. Best-effort; never fails the test.
func waitForSelectedPodsRunning(ctx context.Context, client klient.Client, namespace string) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var pods corev1.PodList
		if err := client.Resources(namespace).List(ctx, &pods,
			resources.WithLabelSelector(npPodLabelKey+"="+npPodLabelVal)); err != nil {
			return
		}
		allRunning := len(pods.Items) > 0
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				allRunning = false
			}
		}
		if allRunning {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// waitForNamespaceGone waits (bounded) for full namespace deletion so a re-run
// doesn't collide with a terminating namespace. Best-effort.
func waitForNamespaceGone(ctx context.Context, client klient.Client, namespace *corev1.Namespace) {
	_ = wait.For(
		conditions.New(client.Resources()).ResourceDeleted(namespace),
		wait.WithTimeout(2*time.Minute),
		wait.WithContext(ctx),
	)
}

// mapDumpPopulated reports whether the node's bundle has a populated
// ebpf-maps-data.txt, asserting well-formedness when it does. It returns false
// (without failing) on an empty dump with a skip reason, so the caller can treat
// "no maps anywhere" as an enforcement-disabled skip.
func mapDumpPopulated(t *testing.T, node string, reader io.Reader) bool {
	files, err := readBundle(reader)
	if err != nil {
		t.Fatal(err)
	}
	ebpfData := files[ebpfDataPath]
	ebpfMapsData := files[ebpfMapsDataPath]
	assert.NotEmpty(t, ebpfData, "%s should be present on node %s", ebpfDataPath, node)

	line, haveLine := selectionLine(ebpfData)
	selected := haveLine && strings.Contains(line, cliSelectedMarker)

	if len(ebpfMapsData) == 0 {
		// Selected but no maps is a defect; a skip reason is enforcement-off.
		if selected {
			t.Errorf("node %s selected a na-cli binary but produced no map dump — collector or agent defect; selection line: %q", node, line)
		} else {
			t.Logf("node %s recorded a skip (not a selection); no map dump expected: %q", node, line)
		}
		return false
	}
	assert.Contains(t, string(ebpfMapsData), mapIDMarker,
		"%s on node %s should contain per-map dump sections", ebpfMapsDataPath, node)
	assert.True(t, selected,
		"node %s produced maps, so the CLI selection line should show a chosen binary; got %q", node, line)
	return true
}

// selectionLine returns the collector's "network-policy CLI selection" line.
func selectionLine(ebpfData []byte) (string, bool) {
	for _, line := range strings.Split(string(ebpfData), "\n") {
		if strings.HasPrefix(line, cliSelectionLinePrefix) {
			return line, true
		}
	}
	return "", false
}
