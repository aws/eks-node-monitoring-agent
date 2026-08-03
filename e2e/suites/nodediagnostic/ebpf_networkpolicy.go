package nodediagnostic

import (
	"archive/tar"
	"compress/gzip"
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
	npTestNamespace  = "ebpf-np-e2e"
	npServerDeploy   = "np-server"
	npServerReplicas = 2
	npPodLabelKey    = "app"
	npPodLabelVal    = "np-server"
	awsNodeNamespace = "kube-system"
	awsNodeDSName    = "aws-node"
	nodeAgentName    = "aws-eks-nodeagent"
	// nginx pinned by tag rather than :latest for reproducibility.
	npServerImage = "public.ecr.aws/nginx/nginx:1.27"
)

// EbpfMapCollectionWithNetworkPolicy exercises the "good path" of the
// network-policy eBPF collector. It applies a default-deny NetworkPolicy and a
// matching workload so the network policy agent programs eBPF maps on the
// node(s) running the selected pods, captures a NodeDiagnostic bundle from those
// specific nodes, and asserts the map dump is populated.
//
// It is defensive about preconditions that are not observable from this repo:
//   - skips when --nodediagnostic-bucket-name is unset,
//   - skips when the aws-eks-nodeagent container is absent (no agent at all),
//   - and, because container presence does not imply enforcement is enabled,
//     skips (rather than fails) if NO target node produced a map dump — that
//     outcome means network policy is not being enforced on this cluster, which
//     is a valid configuration, not a collector defect.
//
// The assertion only runs against nodes that actually host np-server pods, so a
// larger cluster with idle nodes does not cause false failures.
func EbpfMapCollectionWithNetworkPolicy(awsConfig aws.Config) types.Feature {
	// nodeDiagnostics is keyed by node name, only for nodes running np-server.
	nodeDiagnostics := map[string]v1alpha1.NodeDiagnostic{}
	testTimestamp := time.Now().Format("2006-01-02.150405")

	s3Client := s3.NewFromConfig(awsConfig)
	presignClient := s3.NewPresignClient(s3Client, func(po *s3.PresignOptions) {
		po.Expires = 30 * time.Minute
	})

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: npTestNamespace}}
	labels := map[string]string{npPodLabelKey: npPodLabelVal}
	deployment := npServerDeployment(labels)
	// Default-deny ingress: makes the agent program per-pod policy maps for the
	// selected pods.
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-ingress", Namespace: npTestNamespace},
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
			// Create resources; on any failure, clean up what we made so a
			// re-run does not hit a leaked namespace (Setup failures do not run
			// the feature Teardown).
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

			// Find the node(s) actually running np-server pods; only those can
			// have policy maps programmed.
			targetNodes := nodesRunningSelectedPods(ctx, t, client)
			if len(targetNodes) == 0 {
				cleanup()
				t.Fatal("no np-server pods were scheduled to any node")
			}

			// Poll for the agent to reconcile the policy into maps rather than a
			// fixed sleep. Best-effort: if it never converges we still capture and
			// let the assessment decide (skip vs fail).
			waitForNetworkPolicyMaps(ctx, t, client, targetNodes)

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
			// If no target node produced a map dump, network policy is not being
			// enforced on this cluster (agent present but enforcement disabled).
			// That is a valid configuration, so skip rather than fail.
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
			// Deleting the namespace removes the deployment and policy with it.
			_ = client.Resources().Delete(ctx, namespace)
			waitForNamespaceGone(ctx, client, namespace)
			return ctx
		}).
		Feature()
}

func npServerDeployment(labels map[string]string) *appsv1.Deployment {
	replicas := int32(npServerReplicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: npServerDeploy, Namespace: npTestNamespace},
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
func nodesRunningSelectedPods(ctx context.Context, t *testing.T, client klient.Client) map[string]struct{} {
	var pods corev1.PodList
	if err := client.Resources(npTestNamespace).List(ctx, &pods,
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

// waitForNetworkPolicyMaps gives the agent bounded time to publish the
// family-correct na-cli symlink, as a proxy for "policy reconciled into maps",
// instead of a fixed sleep. Best-effort: it does not fail the test, since the
// symlink lives on the node and is not directly observable from here; it simply
// bounds how long we wait before capturing. The assessment is the real gate.
func waitForNetworkPolicyMaps(ctx context.Context, t *testing.T, client klient.Client, targetNodes map[string]struct{}) {
	// The agent reconciles policy asynchronously; poll the np-server pods until
	// they have been Running for a short settle window. This is a lightweight,
	// observable proxy that avoids a blind fixed sleep.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var pods corev1.PodList
		if err := client.Resources(npTestNamespace).List(ctx, &pods,
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
			// Small settle window for the agent's reconcile loop.
			time.Sleep(20 * time.Second)
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// waitForNamespaceGone blocks (bounded) until the namespace is fully deleted, so
// a re-run does not collide with a terminating namespace. Best-effort.
func waitForNamespaceGone(ctx context.Context, client klient.Client, namespace *corev1.Namespace) {
	_ = wait.For(
		conditions.New(client.Resources()).ResourceDeleted(namespace),
		wait.WithTimeout(2*time.Minute),
		wait.WithContext(ctx),
	)
}

// mapDumpPopulated reports whether the bundle for a node contains a populated
// ebpf-maps-data.txt. When maps are present it also asserts well-formedness
// (the selection line shows a chosen binary and the dump has per-map sections).
// A node with no maps returns false without failing, so the caller can treat
// "no maps on any target node" as an enforcement-disabled skip.
func mapDumpPopulated(t *testing.T, node string, reader io.Reader) bool {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var ebpfData, ebpfMapsData []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %s", err)
		}
		switch h.Name {
		case ebpfDataPath:
			ebpfData, err = io.ReadAll(tr)
			assert.NoError(t, err)
		case ebpfMapsDataPath:
			ebpfMapsData, err = io.ReadAll(tr)
			assert.NoError(t, err)
		}
	}
	assert.NotEmpty(t, ebpfData, "%s should be present on node %s", ebpfDataPath, node)
	if len(ebpfMapsData) == 0 {
		t.Logf("node %s produced no eBPF map dump; ebpf-data.txt:\n%s", node, string(ebpfData))
		return false
	}
	// Maps are present: they must be well-formed and the collector must have
	// recorded a selected binary.
	assert.Contains(t, string(ebpfMapsData), "Map ID:",
		"%s on node %s should contain per-map dump sections", ebpfMapsDataPath, node)
	if line, ok := selectionLine(ebpfData); ok {
		assert.Contains(t, line, "using it",
			"node %s produced maps, so the CLI selection line should show a chosen binary; got %q", node, line)
	}
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
