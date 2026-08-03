package nodediagnostic

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
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
	"sigs.k8s.io/e2e-framework/klient/k8s"
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
	awsNodeNamespace = "kube-system"
	awsNodeDSName    = "aws-node"
	nodeAgentName    = "aws-eks-nodeagent"
)

// EbpfMapCollectionWithNetworkPolicy forces the "good path" of the network-policy
// eBPF collector: it applies a NetworkPolicy and matching workload so the network
// policy agent programs eBPF maps (and publishes the family-correct na-cli
// symlink), captures a node bundle, and asserts the map dump is populated.
//
// It skips when the network policy agent is not running on the cluster (the
// aws-node daemonset has no aws-eks-nodeagent container), since without it no
// eBPF maps are programmed and the "good path" cannot be exercised.
func EbpfMapCollectionWithNetworkPolicy(awsConfig aws.Config) types.Feature {
	var nodeDiagnostics []v1alpha1.NodeDiagnostic
	testTimestamp := time.Now().Format("2006-01-02.150405")

	s3Client := s3.NewFromConfig(awsConfig)
	presignClient := s3.NewPresignClient(s3Client, func(po *s3.PresignOptions) {
		po.Expires = 30 * time.Minute
	})

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: npTestNamespace}}

	labels := map[string]string{"app": "np-server"}
	deployment := npServerDeployment(labels)
	// Default-deny ingress: forces the agent to program per-pod policy maps.
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
			for _, obj := range []k8s.Object{namespace, deployment, policy} {
				if err := client.Resources().Create(ctx, obj); err != nil {
					t.Fatalf("failed to create %T: %s", obj, err)
				}
			}
			// Wait for the workload so the agent has pods to program policy for.
			if err := wait.For(
				conditions.New(client.Resources()).ResourceMatch(deployment, func(object k8s.Object) bool {
					d := object.(*appsv1.Deployment)
					return d.Status.ReadyReplicas == npServerReplicas
				}),
				wait.WithTimeout(3*time.Minute),
				wait.WithContext(ctx),
			); err != nil {
				t.Fatalf("np-server deployment did not become ready: %s", err)
			}
			// Give the agent time to reconcile the policy into eBPF maps and
			// publish the family-correct symlink before the bundle is captured.
			time.Sleep(30 * time.Second)

			var nodes corev1.NodeList
			if err := client.Resources().List(ctx, &nodes); err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes.Items {
				if node.DeletionTimestamp != nil {
					continue
				}
				nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-ebpf-%s.tgz", testTimestamp, node.Name))
				presignedRequest, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
					Bucket: &nodeDiagnosticLogBucket,
					Key:    &nodeDiagnosticLogKey,
				})
				if err != nil {
					t.Fatalf("failed to create presigned s3 PUT: %s", err)
				}
				nodeDiagnostic := v1alpha1.NodeDiagnostic{
					ObjectMeta: metav1.ObjectMeta{Name: node.Name},
					Spec: v1alpha1.NodeDiagnosticSpec{
						LogCapture: &v1alpha1.LogCapture{
							UploadDestination: v1alpha1.UploadDestination(presignedRequest.URL),
						},
					},
				}
				if err := client.Resources().Create(ctx, &nodeDiagnostic); err != nil {
					t.Fatal(err)
				}
				nodeDiagnostics = append(nodeDiagnostics, nodeDiagnostic)
			}
			if len(nodeDiagnostics) == 0 {
				t.Fatal("no non-terminating nodes were found in the cluster")
			}
			return ctx
		}).
		Assess("MapDumpPopulated", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
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

					nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-ebpf-%s.tgz", testTimestamp, nodeDiagnostic.Name))
					objectResponse, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
						Bucket: &nodeDiagnosticLogBucket,
						Key:    &nodeDiagnosticLogKey,
					})
					if err != nil {
						t.Fatal(err)
					}
					assertEbpfMapDumpPresent(t, objectResponse.Body)
				})
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			for _, nodeDiagnostic := range nodeDiagnostics {
				_ = client.Resources().Delete(ctx, &nodeDiagnostic)
			}
			// Best-effort cleanup; deleting the namespace removes the deployment
			// and policy with it.
			_ = client.Resources().Delete(ctx, namespace)
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
						Image: "public.ecr.aws/nginx/nginx:latest",
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

// assertEbpfMapDumpPresent requires that the bundle contains a populated
// ebpf-maps-data.txt and that ebpf-data.txt records a selected (not skipped)
// na-cli binary.
func assertEbpfMapDumpPresent(t *testing.T, reader io.Reader) {
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
	if !assert.NotEmpty(t, ebpfData, "%s should be present", ebpfDataPath) {
		return
	}
	assert.Contains(t, string(ebpfData), cliSelectedMarker,
		"with the network policy agent running and a policy applied, a na-cli binary should be selected; ebpf-data.txt:\n%s", string(ebpfData))
	assert.NotEmpty(t, ebpfMapsData,
		"%s should be populated when network policy is enforced", ebpfMapsDataPath)
	if len(ebpfMapsData) > 0 {
		assert.Contains(t, string(ebpfMapsData), "Map ID:",
			"%s should contain per-map dump sections", ebpfMapsDataPath)
	}
}
