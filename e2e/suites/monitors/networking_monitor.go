package monitors

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/validation"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
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
func NetworkingMonitor(awsCfg aws.Config) types.Feature {
	var targetNode *corev1.Node
	ec2Client := ec2.NewFromConfig(awsCfg)
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
					Image:   TestImage,
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
			if err := cfg.Client().Resources().List(ctx, &nodeList,
				resources.WithLabelSelector(computeTypeLabelKey+"!="+computeTypeFargate),
			); err != nil {
				t.Fatal(err)
			}
			targetNode = &nodeList.Items[0]
			t.Logf("targeting node %q for test", targetNode.Name)
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
		Assess("LinkLocalInsightPublished", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			scraped, err := scrapeAgentMetrics(ctx, t, cfg, targetNode)
			if err != nil {
				t.Fatalf("failed to scrape the agent's metrics on %q: %s", targetNode.Name, err)
			}
			eniID, err := linkLocalInsightENI(scraped)
			if err != nil {
				t.Fatalf("failed to verify the %s/%s insight: %s",
					insightTypeNetworkAllowanceExceeded, insightReasonLinkLocal, err)
			}
			// the flood pod is host-networked, so the traffic left the node's
			// primary ENI. naming any other one would mean the monitor mapped
			// an interface to the wrong ENI, which the ID's shape alone cannot
			// catch on a node that has several.
			primaryENI, err := primaryENIID(ctx, ec2Client, targetNode)
			if err != nil {
				t.Fatalf("failed to look up the primary ENI of %q: %s", targetNode.Name, err)
			}
			if eniID != primaryENI {
				t.Fatalf("insight names %q, want the node's primary ENI %q", eniID, primaryENI)
			}
			t.Logf("node %q published the link-local allowance insight for its primary ENI %q",
				targetNode.Name, eniID)
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

// The eks_insight the networking monitor publishes for an ENI exceeding its
// link-local allowance. These are the published contract rather than values
// imported from the monitor, so that renaming one fails this test.
const (
	insightMetricName                   = "eks_insight"
	insightTypeNetworkAllowanceExceeded = "NetworkAllowanceExceeded"
	insightReasonLinkLocal              = "LinkLocal"
	insightLabelENIID                   = "resource_eni_id"
)

// The port the agent serves metrics on, which differs by how it was
// installed: the chart passes --metrics-address=:8003, and EKS Auto Mode runs
// it as a host process on 8801.
const (
	defaultChartMetricsPort = "8003"
	autoMetricsPort         = "8801"
)

const computeTypeFargate = "fargate"

// agentMetricsPort returns the port the agent on the node serves metrics on,
// chosen from the node's compute type because under Auto Mode the agent is a
// host process rather than a pod whose arguments could be read.
func agentMetricsPort(node *corev1.Node) string {
	if node.Labels[computeTypeLabelKey] == computeTypeAuto {
		return autoMetricsPort
	}
	return defaultChartMetricsPort
}

// agentScrapeTimeout allows one further check interval beyond the event, since
// the insight is published by the same tick that sends the Warning.
const agentScrapeTimeout = 7 * time.Minute

// eniIDPattern guards against the monitor publishing an interface name, or an
// empty label, where an ENI ID belongs.
var eniIDPattern = regexp.MustCompile(`^eni-[0-9a-f]+$`)

// scrapeAgentMetrics returns the agent's exposition output from the named
// node, waiting until an eks_insight series appears in it.
func scrapeAgentMetrics(ctx context.Context, t *testing.T, cfg *envconf.Config, node *corev1.Node) (string, error) {
	port := agentMetricsPort(node)
	t.Logf("scraping the agent on %q at 127.0.0.1:%s", node.Name, port)
	pod := agentScrapePod(node.Name, port)
	if err := cfg.Client().Resources().Create(ctx, &pod); err != nil {
		return "", err
	}
	defer func() {
		if err := cfg.Client().Resources().Delete(ctx, &pod); err != nil {
			t.Logf("deleting the scrape pod: %s", err)
		}
	}()

	var phase corev1.PodPhase
	if err := wait.For(
		func(ctx context.Context) (bool, error) {
			var current corev1.Pod
			if err := cfg.Client().Resources().Get(ctx, pod.Name, pod.Namespace, &current); err != nil {
				return false, nil
			}
			phase = current.Status.Phase
			return phase == corev1.PodSucceeded || phase == corev1.PodFailed, nil
		},
		wait.WithTimeout(agentScrapeTimeout),
		wait.WithInterval(15*time.Second),
	); err != nil {
		return "", fmt.Errorf("the scrape pod did not finish: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg.Client().RESTConfig())
	if err != nil {
		return "", err
	}
	logs, err := clientset.CoreV1().Pods(pod.Namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the scrape pod's output: %w", err)
	}
	if phase == corev1.PodFailed {
		return "", fmt.Errorf("the scrape pod failed: %s", strings.TrimSpace(string(logs)))
	}
	return string(logs), nil
}

// agentScrapeProbeScript fetches the agent's metrics until an eks_insight
// series appears, then prints that scrape and nothing else, so the caller can
// parse it as exposition output.
//
// The request goes over bash's /dev/tcp rather than curl, as the flood above
// does with /dev/udp, because neither the test image nor an EKS Auto Mode host
// carries an HTTP client. Each attempt runs in a subshell so its socket is
// closed before the next, and stderr is discarded throughout: the pod's log is
// the exposition output, so a refused connection on an early attempt is kept
// out of it.
func agentScrapeProbeScript(port string) string {
	return fmt.Sprintf(`
for _ in $(seq 1 24); do
  body=$(
    exec 3<>/dev/tcp/127.0.0.1/%s &&
    printf 'GET /metrics HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3 &&
    sed '1,/^\r*$/d' <&3
  ) 2>/dev/null
  if printf '%%s' "$body" | grep -q '^%s{'; then
    printf '%%s\n' "$body"
    exit 0
  fi
  sleep 15
done
echo "no %s series on 127.0.0.1:%s after 6 minutes" >&2
exit 1
`, port, insightMetricName, insightMetricName, port)
}

func agentScrapePod(nodeName, port string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linklocal-scrape",
			Namespace: corev1.NamespaceDefault,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			// the agent listens in the node's network namespace, whether it
			// runs as a host-networked pod or as a host process.
			HostNetwork:   true,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "scrape",
					Command: []string{"/bin/bash", "-c", agentScrapeProbeScript(port)},
					Image:   TestImage,
				},
			},
		},
	}
}

// linkLocalInsightENI returns the ENI named by the link-local allowance
// insight in the exposition output, failing if the series is absent or
// malformed.
func linkLocalInsightENI(scraped string) (string, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(scraped))
	if err != nil {
		return "", fmt.Errorf("parse the scraped metrics: %w", err)
	}
	family, published := families[insightMetricName]
	if !published {
		return "", fmt.Errorf("no %s series was scraped", insightMetricName)
	}
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["type"] != insightTypeNetworkAllowanceExceeded || labels["reason"] != insightReasonLinkLocal {
			continue
		}
		eniID := labels[insightLabelENIID]
		if !eniIDPattern.MatchString(eniID) {
			return "", fmt.Errorf("insight names %s=%q, which is not an ENI ID", insightLabelENIID, eniID)
		}
		if value := metric.GetGauge().GetValue(); value != 1 {
			return "", fmt.Errorf("insight for %q has value %v, want 1", eniID, value)
		}
		return eniID, nil
	}
	return "", fmt.Errorf("%s was scraped, but none for %s/%s",
		insightMetricName, insightTypeNetworkAllowanceExceeded, insightReasonLinkLocal)
}

// primaryENIID returns the ID of the node's primary ENI, which is where a
// host-networked pod's traffic leaves from.
//
// Device index is scoped per network card, so on an instance with several
// cards more than one ENI has device index 0 and DescribeInstances does not
// promise an order between them. Both indexes have to be zero to name the
// primary one.
func primaryENIID(ctx context.Context, ec2Client *ec2.Client, node *corev1.Node) (string, error) {
	instanceID, err := validation.ParseProviderID(node.Spec.ProviderID)
	if err != nil {
		return "", fmt.Errorf("parse the provider ID of %q: %w", node.Name, err)
	}
	described, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", fmt.Errorf("describe instance %q: %w", instanceID, err)
	}
	for _, reservation := range described.Reservations {
		for _, instance := range reservation.Instances {
			for _, eni := range instance.NetworkInterfaces {
				if eni.Attachment == nil {
					continue
				}
				if aws.ToInt32(eni.Attachment.DeviceIndex) != 0 || aws.ToInt32(eni.Attachment.NetworkCardIndex) != 0 {
					continue
				}
				return aws.ToString(eni.NetworkInterfaceId), nil
			}
		}
	}
	return "", fmt.Errorf("instance %q has no ENI at device index 0 of network card 0", instanceID)
}
