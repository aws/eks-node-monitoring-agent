package monitors

import (
	"context"
	"testing"
	"time"

	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

// linkLocalFloodScript sends DNS queries to the Amazon-provided DNS server at
// its link-local address (169.254.169.253) at roughly 5000 packets/s, well
// above the 1024 PPS per-ENI allowance for link-local services. The excess is
// dropped by EC2 and counted in the ENA driver's linklocal_allowance_exceeded
// stat, which the networking monitor reads via `ethtool -S`.
//
// Each batch writes 200 datagrams over bash's /dev/udp redirection (the
// minimal image has no python or dig) then sleeps 20ms, which caps the rate so
// the pod uses little CPU and doesn't flood the node any harder than needed.
const linkLocalFloodScript = `
# DNS query: id=0x1234, RD, 1 question, A record for amazon.com.
Q='\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x06amazon\x03com\x00\x00\x01\x00\x01'
exec 3>/dev/udp/169.254.169.253/53
echo "flooding 169.254.169.253:53"
while :; do
  for _ in {1..200}; do printf "$Q" >&3; done 2>/dev/null
  sleep 0.02
done
`

// linkLocalAllowanceDetectionTimeout covers the worst case: the ethtool check
// runs every 5 minutes (after up to 1 minute of startup jitter), and a spike
// is only detected by comparing two readings, so one full interval must pass
// after the flood starts plus up to another for the next tick to land.
const linkLocalAllowanceDetectionTimeout = 11 * time.Minute

// NetworkingMonitor verifies the ethtool allowance-exceeded check detects
// throttling end to end.
func NetworkingMonitor() types.Feature {
	var targetNode *corev1.Node
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linklocal-flood",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			// host networking sends the traffic from the node's primary ENI,
			// which has a direct route to the link-local address.
			HostNetwork:   true,
			RestartPolicy: corev1.RestartPolicyNever,
			// the flood loops forever, so skip the default 30s grace period.
			TerminationGracePeriodSeconds: new(int64),
			Containers: []corev1.Container{
				{
					Name:    "flood",
					Image:   "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
					Command: []string{"/bin/bash", "-c", linkLocalFloodScript},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
					},
				},
			},
		},
	}

	return features.New("NetworkingMonitor").
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
		Assess("LinkLocalExceeded", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			startTime := metav1.Now()
			if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).PodRunning(&pod),
				wait.WithTimeout(2*time.Minute),
			); err != nil {
				t.Fatalf("flood pod did not start: %s", err)
			}
			if err := wait.For(
				eventWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, nodeconditions.NetworkingReady, "LinkLocalExceeded"),
				wait.WithTimeout(linkLocalAllowanceDetectionTimeout),
				wait.WithInterval(15*time.Second),
			); err != nil {
				t.Fatalf("failed to verify event %q was present: %s", "LinkLocalExceeded", err)
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
