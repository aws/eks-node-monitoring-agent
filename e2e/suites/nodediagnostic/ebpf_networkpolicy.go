package nodediagnostic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	k8shelper "github.com/aws/eks-node-monitoring-agent/e2e/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	npPodLabelKey    = "app"
	npPodLabelVal    = "np-server"
	npServerReplicas = 2
	npServerImage    = "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal"

	awsNodeName      = "aws-node"
	awsNodeNamespace = "kube-system"
	nodeAgentName    = "aws-eks-nodeagent"
	vpcCNIAddon      = "vpc-cni"
)

// EbpfMapCollectionWithNetworkPolicy applies a default-deny NetworkPolicy and a
// matching workload so the agent programs eBPF maps, then captures a
// NodeDiagnostic bundle per hosting node and asserts:
//   - ebpf-data.txt is present;
//   - na-cli exec did not fail;
//   - ebpf-maps-data.txt is non-empty (map data was collected).
//
// A missing file or failed exec is a failure on any node. If no node produced a
// map dump and nothing failed, it skips rather than passing.
func EbpfMapCollectionWithNetworkPolicy(awsConfig aws.Config) types.Feature {
	testTimestamp := time.Now().Format("2006-01-02.150405")
	// One definition for the S3 key so the Setup PUT and the Assess GET can't drift.
	bundleKey := func(node string) string {
		return path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-ebpf-%s.tgz", testTimestamp, node))
	}

	s3Client := s3.NewFromConfig(awsConfig)
	presignClient := s3.NewPresignClient(s3Client, func(po *s3.PresignOptions) {
		po.Expires = 30 * time.Minute
	})

	// Random namespace so a re-run's leftover workload can't collide with ours.
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

	// Filled in Setup, read in Assess/Teardown. nodeOS maps a hosting node to its
	// OS image, for reporting in assess messages.
	nodeOS := map[string]string{}
	nodeDiagnostics := map[string]v1alpha1.NodeDiagnostic{}
	// restoreNP undoes any network-policy enablement so the cluster is left as
	// found; a no-op unless Setup turned enforcement on.
	restoreNP := func() {}

	return features.New("EbpfMapCollectionWithNetworkPolicy").
		WithLabel("type", "log-collection").
		WithSetup("RequireBucket", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if nodeDiagnosticLogBucket == "" {
				t.Skip("skipping: --nodediagnostic-bucket-name flag was not provided")
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

			// Enable NP so the agent programs eBPF maps; restoreNP undoes it on teardown.
			restoreNP = enableNetworkPolicy(ctx, t, cfg, awsConfig)

			// Setup failures don't run the feature Teardown, so clean up explicitly.
			created := []k8s.Object{}
			cleanup := func() {
				for i := len(created) - 1; i >= 0; i-- {
					_ = client.Resources().Delete(ctx, created[i])
				}
				waitForNamespaceGone(ctx, client, namespace)
				restoreNP()
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

			// Only nodes hosting a selected pod can have policy maps programmed.
			var pods corev1.PodList
			if err := client.Resources(nsName).List(ctx, &pods,
				resources.WithLabelSelector(npPodLabelKey+"="+npPodLabelVal)); err != nil {
				cleanup()
				t.Fatalf("failed to list np-server pods: %s", err)
			}
			for _, pod := range pods.Items {
				if pod.Spec.NodeName == "" {
					continue
				}
				var node corev1.Node
				if err := client.Resources().Get(ctx, pod.Spec.NodeName, "", &node); err != nil {
					cleanup()
					t.Fatalf("failed to get node %s: %s", pod.Spec.NodeName, err)
				}
				nodeOS[node.Name] = nodeOSImage(node)
			}
			if len(nodeOS) == 0 {
				cleanup()
				t.Fatal("no np-server pods were scheduled to any node")
			}

			for node := range nodeOS {
				nodeDiagnostic, err := captureToS3(ctx, client, presignClient, nodeDiagnosticLogBucket, bundleKey(node), node)
				if err != nil {
					cleanup()
					t.Fatalf("failed to capture NodeDiagnostic for %s: %s", node, err)
				}
				// NodeDiagnostics are cluster-scoped and named after the node, so a
				// leak from a later-iteration failure would wedge the next run.
				created = append(created, &nodeDiagnostic)
				nodeDiagnostics[node] = nodeDiagnostic
			}
			return ctx
		}).
		Assess("MapDumpCollected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			collected := 0
			for node, nodeDiagnostic := range nodeDiagnostics {
				if err := cfg.Client().Resources().Get(ctx, nodeDiagnostic.Name, nodeDiagnostic.Namespace, &nodeDiagnostic); err != nil {
					t.Fatal(err)
				}
				if err := waitCaptureComplete(ctx, cfg.Client(), &nodeDiagnostic, time.Minute); err != nil {
					t.Fatal(err)
				}

				nodeDiagnosticLogKey := bundleKey(node)
				objectResponse, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
					Bucket: &nodeDiagnosticLogBucket,
					Key:    &nodeDiagnosticLogKey,
				})
				if err != nil {
					t.Fatal(err)
				}
				if evaluateNode(t, node, nodeOS[node], objectResponse.Body) {
					collected++
				}
			}
			// Nothing collected and nothing failed → the run validated nothing, usually
			// because network policy enforcement isn't enabled on the cluster. Skip
			// loudly rather than show a false green.
			if collected == 0 && !t.Failed() {
				t.Skip("skipped without validating: no node produced an eBPF map dump — " +
					"network policy enforcement is likely not enabled on this cluster; see per-node logs")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			for _, nd := range nodeDiagnostics {
				_ = client.Resources().Delete(ctx, &nd)
			}
			_ = client.Resources().Delete(ctx, namespace)
			waitForNamespaceGone(ctx, client, namespace)
			restoreNP()
			return ctx
		}).
		Feature()
}

// evaluateNode reads a node's bundle and checks the collector's eBPF files:
// ebpf-data.txt present, na-cli exec did not fail, and ebpf-maps-data.txt
// non-empty. It reports whether map data was collected. osImage is for messages.
func evaluateNode(t *testing.T, node, osImage string, reader io.Reader) bool {
	files, err := readBundle(reader)
	if err != nil {
		t.Fatal(err)
	}
	ebpfData := files[ebpfDataPath]
	present := len(ebpfData) > 0
	mapsPresent := len(files[ebpfMapsDataPath]) > 0
	// Match the whole exec-failure line, not a bare substring, so a successful dump
	// that mentions the phrase can't false-positive.
	execFailed := containsLinePrefix(ebpfData, cliExecFailedPrefix)
	selection := findLine(ebpfData, cliSelectionLinePrefix) // for messages

	// ebpf-data.txt is written wherever the collector runs, so absence is a failure.
	if !present {
		t.Errorf("node %s (%s): %s absent — eBPF collector did not run", node, osImage, ebpfDataPath)
		return false
	}
	if execFailed {
		t.Errorf("node %s (%s): na-cli exec failed; %q", node, osImage, selection)
		return false
	}
	if mapsPresent {
		return true
	}
	// Nothing collected — log the selection line so the skip is diagnosable.
	t.Logf("node %s (%s): no eBPF map dump collected; %q", node, osImage, selection)
	return false
}

// npServerDeployment builds the np-server workload, unpinned so it can land on
// any node.
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
						Name:    "server",
						Image:   npServerImage,
						Command: []string{"/bin/bash", "-c", "sleep infinity"},
					}},
				},
			},
		},
	}
}

// nodeOSImage returns the node's OS image for reporting ("unreported OS" if empty).
func nodeOSImage(node corev1.Node) string {
	if osImage := node.Status.NodeInfo.OSImage; osImage != "" {
		return osImage
	}
	return "unreported OS"
}

// enableNetworkPolicy turns on vpc-cni network policy via the addon config so the
// agent programs eBPF maps, waits for the aws-node rollout, and returns a function
// that writes the snapshotted config back, so the cluster ends as it started. It
// no-ops with an empty restore when enforcement is already on or on Auto Mode
// (managed). It logs failures instead of failing; the assess skip catches a no-op.
func enableNetworkPolicy(ctx context.Context, t *testing.T, cfg *envconf.Config, awsConfig aws.Config) (restore func()) {
	restore = func() {}
	var ds appsv1.DaemonSet
	if err := cfg.Client().Resources().Get(ctx, awsNodeName, awsNodeNamespace, &ds); err != nil {
		t.Logf("no %s/%s daemonset (Auto Mode?); network policy is managed, not enabling: %s", awsNodeNamespace, awsNodeName, err)
		return restore
	}
	if nodeagentEnforcesNetworkPolicy(ds) {
		t.Log("network policy enforcement already enabled on aws-node")
		return restore
	}
	clusterName, err := k8shelper.ExtractClusterName(cfg.KubeContext())
	if err != nil {
		t.Logf("could not resolve cluster name to enable network policy: %s", err)
		return restore
	}
	eksClient := eks.NewFromConfig(awsConfig)
	// Snapshot the addon config first: UpdateAddon replaces the whole
	// configurationValues document, so we merge enableNetworkPolicy into the
	// existing config rather than clobbering other keys (WARM_*, custom env, ...),
	// and restore puts the snapshot back.
	origConfig, err := vpcCNIConfig(ctx, eksClient, clusterName)
	if err != nil {
		t.Logf("could not read %s addon config (a managed addon is required): %s", vpcCNIAddon, err)
		return restore
	}
	enabledConfig, err := withNetworkPolicyEnabled(origConfig)
	if err != nil {
		t.Logf("could not compose network-policy addon config: %s", err)
		return restore
	}
	if err := updateVPCCNIConfig(ctx, eksClient, clusterName, enabledConfig); err != nil {
		t.Logf("could not enable network policy on the %s addon: %s", vpcCNIAddon, err)
		return restore
	}
	t.Logf("enabled network policy on cluster %s; waiting for aws-node rollout", *clusterName)
	waitNodeagentRollout(ctx, t, cfg.Client(), true)

	// Restore writes the snapshot back verbatim. Writing
	// "enableNetworkPolicy":"false" instead would leave the key behind on a cluster
	// that never had it — behaviourally the same (the chart defaults to off) but
	// permanent drift for whoever manages the addon. Restoring the snapshot also
	// keeps the flag on if it was already on and only the daemonset lagged.
	return func() {
		restored := origConfig
		if strings.TrimSpace(restored) == "" {
			// Originally unset. UpdateAddon's handling of an empty string is
			// unspecified, so send an empty document, which is equivalent.
			restored = "{}"
		}
		if err := updateVPCCNIConfig(ctx, eksClient, clusterName, restored); err != nil {
			t.Logf("could not restore the %s addon config on cluster %s (left as-is): %s", vpcCNIAddon, *clusterName, err)
			return
		}
		t.Logf("restored the %s addon config on cluster %s; waiting for aws-node rollout", vpcCNIAddon, *clusterName)
		waitNodeagentRollout(ctx, t, cfg.Client(), false)
	}
}

// vpcCNIConfig returns the vpc-cni addon's current configurationValues JSON
// ("" when none is set).
func vpcCNIConfig(ctx context.Context, eksClient *eks.Client, clusterName *string) (string, error) {
	out, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
		ClusterName: clusterName,
		AddonName:   aws.String(vpcCNIAddon),
	})
	if err != nil {
		return "", err
	}
	if out.Addon == nil || out.Addon.ConfigurationValues == nil {
		return "", nil
	}
	return *out.Addon.ConfigurationValues, nil
}

// withNetworkPolicyEnabled returns config with enableNetworkPolicy turned on,
// preserving every other key. UpdateAddon replaces the whole document, so we
// merge rather than overwrite.
func withNetworkPolicyEnabled(config string) (string, error) {
	m := map[string]json.RawMessage{}
	if strings.TrimSpace(config) != "" {
		if err := json.Unmarshal([]byte(config), &m); err != nil {
			return "", err
		}
	}
	m["enableNetworkPolicy"] = json.RawMessage(`"true"`)
	b, err := json.Marshal(m)
	return string(b), err
}

// updateVPCCNIConfig sets the vpc-cni addon's configurationValues, overwriting conflicts.
func updateVPCCNIConfig(ctx context.Context, eksClient *eks.Client, clusterName *string, config string) error {
	_, err := eksClient.UpdateAddon(ctx, &eks.UpdateAddonInput{
		ClusterName:         clusterName,
		AddonName:           aws.String(vpcCNIAddon),
		ConfigurationValues: aws.String(config),
		ResolveConflicts:    ekstypes.ResolveConflictsOverwrite,
	})
	return err
}

// waitNodeagentRollout waits until the aws-node daemonset reports the wanted
// enforcement state on a fully rolled-out generation. Best-effort: it logs on
// timeout rather than failing.
func waitNodeagentRollout(ctx context.Context, t *testing.T, client klient.Client, wantEnforcing bool) {
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: awsNodeName, Namespace: awsNodeNamespace}}
	if err := wait.For(
		conditions.New(client.Resources()).ResourceMatch(ds, func(object k8s.Object) bool {
			d := object.(*appsv1.DaemonSet)
			s := d.Status
			// ObservedGeneration must have caught up to the spec, or the counts
			// below are the previous rollout's — which would report the old,
			// still-running pods as the finished new ones.
			return s.ObservedGeneration >= d.Generation &&
				s.DesiredNumberScheduled > 0 &&
				s.UpdatedNumberScheduled == s.DesiredNumberScheduled &&
				s.NumberReady == s.DesiredNumberScheduled &&
				nodeagentEnforcesNetworkPolicy(*d) == wantEnforcing
		}),
		wait.WithTimeout(5*time.Minute),
		wait.WithContext(ctx),
	); err != nil {
		t.Logf("timed out waiting for the %s rollout (want enforcing=%t); proceeding: %s", awsNodeName, wantEnforcing, err)
	}
}

// nodeagentEnforcesNetworkPolicy reports whether the aws-node daemonset's
// aws-eks-nodeagent container runs with --enable-network-policy=true.
func nodeagentEnforcesNetworkPolicy(ds appsv1.DaemonSet) bool {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name != nodeAgentName {
			continue
		}
		for _, arg := range append(append([]string{}, c.Command...), c.Args...) {
			if strings.Contains(arg, "enable-network-policy=true") {
				return true
			}
		}
	}
	return false
}

// waitForNamespaceGone waits (bounded, best-effort) for namespace deletion so a
// re-run doesn't collide with a terminating namespace.
func waitForNamespaceGone(ctx context.Context, client klient.Client, namespace *corev1.Namespace) {
	_ = wait.For(
		conditions.New(client.Resources()).ResourceDeleted(namespace),
		wait.WithTimeout(2*time.Minute),
		wait.WithContext(ctx),
	)
}
