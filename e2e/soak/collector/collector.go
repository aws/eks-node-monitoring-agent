// Package collector samples the memory and resource signals of every NMA pod
// on the soak cluster: Go heap and runtime memory and goroutines from the
// agent's own /metrics endpoint, RSS and open FDs from its process collector,
// the container working set from kubelet cAdvisor, and restarts from pod
// status.
package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

// Signal names. They double as the CloudWatch metric names.
const (
	// HeapInuse is go_memstats_heap_inuse_bytes: bytes in in-use heap spans.
	HeapInuse = "HeapInuseBytes"
	// HeapAlloc is go_memstats_heap_alloc_bytes: bytes of allocated heap objects.
	HeapAlloc = "HeapAllocBytes"
	// RuntimeSys is go_memstats_sys_bytes: all memory the Go runtime obtained
	// from the OS.
	RuntimeSys = "RuntimeSysBytes"
	// RSS is process_resident_memory_bytes. It includes file-backed pages,
	// such as the binary and memory-mapped journal files, which the kernel can
	// reclaim, so it is published for dashboards but not used by the leak gate.
	RSS = "ResidentMemoryBytes"
	// ContainerRSS is the cAdvisor container_memory_rss of the agent
	// container: its anonymous memory, without file-backed pages.
	ContainerRSS = "ContainerRSSBytes"
	// WorkingSet is the cAdvisor container_memory_working_set_bytes of the
	// agent container, the value the kubelet compares to the memory limit.
	WorkingSet = "WorkingSetBytes"
	// Goroutines is go_goroutines.
	Goroutines = "Goroutines"
	// OpenFDs is process_open_fds.
	OpenFDs = "OpenFDs"
	// Restarts is the agent container's restart count.
	Restarts = "Restarts"
	// OOMKilled is 1 when the agent container's last termination was an OOM kill.
	OOMKilled = "OOMKilled"
)

// MemorySignals are the byte-valued signals.
var MemorySignals = []string{HeapInuse, HeapAlloc, RuntimeSys, RSS, WorkingSet, ContainerRSS}

// agentMetrics maps the agent's Prometheus metric names to signals.
var agentMetrics = map[string]string{
	"go_memstats_heap_inuse_bytes":  HeapInuse,
	"go_memstats_heap_alloc_bytes":  HeapAlloc,
	"go_memstats_sys_bytes":         RuntimeSys,
	"process_resident_memory_bytes": RSS,
	"go_goroutines":                 Goroutines,
	"process_open_fds":              OpenFDs,
}

const (
	DefaultAgentNamespace = "kube-system"
	DefaultAgentContainer = "eks-node-monitoring-agent"
	DefaultAgentSelector  = "app.kubernetes.io/name=eks-node-monitoring-agent"
	// DefaultMetricsPort matches the chart's --metrics-address=:8003.
	DefaultMetricsPort = 8003
)

// Sample is one reading of every signal for the NMA pod on a node.
type Sample struct {
	Time time.Time
	Node string
	Pod  string
	// Pool is the node's soak pool label (workload.SoakPool or
	// workload.ChurnPool), empty for nodes outside the soak pools.
	Pool   string
	Values map[string]float64
}

// Fetcher retrieves raw Prometheus text from the cluster.
type Fetcher interface {
	// AgentMetrics returns the agent pod's /metrics output.
	AgentMetrics(ctx context.Context, pod *corev1.Pod, port int) ([]byte, error)
	// CadvisorMetrics returns the node's kubelet /metrics/cadvisor output.
	CadvisorMetrics(ctx context.Context, node string) ([]byte, error)
}

// apiServerProxy fetches metrics through the API server's pod and node proxy,
// so no agent or kubelet port has to be reachable from the collector.
type apiServerProxy struct {
	client kubernetes.Interface
}

// NewAPIServerFetcher returns a Fetcher that proxies through the API server.
// The caller needs get on pods/proxy and nodes/proxy.
func NewAPIServerFetcher(client kubernetes.Interface) Fetcher {
	return apiServerProxy{client: client}
}

func (f apiServerProxy) AgentMetrics(ctx context.Context, pod *corev1.Pod, port int) ([]byte, error) {
	return f.client.CoreV1().RESTClient().Get().
		Namespace(pod.Namespace).Resource("pods").
		Name(pod.Name + ":" + strconv.Itoa(port)).
		SubResource("proxy").Suffix("metrics").
		DoRaw(ctx)
}

func (f apiServerProxy) CadvisorMetrics(ctx context.Context, node string) ([]byte, error) {
	return f.client.CoreV1().RESTClient().Get().
		Resource("nodes").Name(node).
		SubResource("proxy").Suffix("metrics", "cadvisor").
		DoRaw(ctx)
}

// Config selects the agent pods to sample.
type Config struct {
	Namespace   string
	Selector    string
	Container   string
	MetricsPort int
}

// DefaultConfig matches the agent as installed by the chart or EKS add-on.
func DefaultConfig() Config {
	return Config{
		Namespace:   DefaultAgentNamespace,
		Selector:    DefaultAgentSelector,
		Container:   DefaultAgentContainer,
		MetricsPort: DefaultMetricsPort,
	}
}

// Collector samples every agent pod.
type Collector struct {
	client  kubernetes.Interface
	fetcher Fetcher
	cfg     Config
	now     func() time.Time
}

// New returns a Collector.
func New(client kubernetes.Interface, fetcher Fetcher, cfg Config) *Collector {
	return &Collector{client: client, fetcher: fetcher, cfg: cfg, now: time.Now}
}

// Collect samples every scheduled agent pod. A pod that can't be sampled is
// skipped and its error returned alongside the samples that succeeded.
func (c *Collector) Collect(ctx context.Context) ([]Sample, error) {
	pods, err := c.client.CoreV1().Pods(c.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.cfg.Selector})
	if err != nil {
		return nil, fmt.Errorf("listing agent pods: %w", err)
	}
	nodes, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	pools := make(map[string]string, len(nodes.Items))
	for _, node := range nodes.Items {
		pools[node.Name] = node.Labels[workload.PoolLabel]
	}

	var samples []Sample
	var errs []error
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil {
			continue
		}
		sample, err := c.sample(ctx, pod)
		if err != nil {
			errs = append(errs, fmt.Errorf("sampling %s on %s: %w", pod.Name, pod.Spec.NodeName, err))
			continue
		}
		sample.Pool = pools[pod.Spec.NodeName]
		samples = append(samples, sample)
	}
	return samples, errors.Join(errs...)
}

func (c *Collector) sample(ctx context.Context, pod *corev1.Pod) (Sample, error) {
	sample := Sample{
		Time:   c.now(),
		Node:   pod.Spec.NodeName,
		Pod:    pod.Name,
		Values: map[string]float64{},
	}

	status, ok := containerStatus(pod, c.cfg.Container)
	if !ok {
		return sample, fmt.Errorf("no status for container %q", c.cfg.Container)
	}
	sample.Values[Restarts] = float64(status.RestartCount)
	sample.Values[OOMKilled] = 0
	if t := status.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
		sample.Values[OOMKilled] = 1
	}

	raw, err := c.fetcher.AgentMetrics(ctx, pod, c.cfg.MetricsPort)
	if err != nil {
		return sample, fmt.Errorf("fetching agent metrics: %w", err)
	}
	families, err := parse(raw)
	if err != nil {
		return sample, fmt.Errorf("parsing agent metrics: %w", err)
	}
	for metricName, signal := range agentMetrics {
		if v, ok := firstValue(families[metricName], nil); ok {
			sample.Values[signal] = v
		}
	}
	for _, signal := range agentMetrics {
		if _, ok := sample.Values[signal]; !ok {
			return sample, fmt.Errorf("agent metrics are missing %s", signal)
		}
	}

	raw, err = c.fetcher.CadvisorMetrics(ctx, pod.Spec.NodeName)
	if err != nil {
		return sample, fmt.Errorf("fetching cadvisor metrics: %w", err)
	}
	families, err = parse(raw)
	if err != nil {
		return sample, fmt.Errorf("parsing cadvisor metrics: %w", err)
	}
	container := map[string]string{
		"namespace": pod.Namespace,
		"pod":       pod.Name,
		"container": c.cfg.Container,
	}
	for metricName, signal := range map[string]string{
		"container_memory_working_set_bytes": WorkingSet,
		"container_memory_rss":               ContainerRSS,
	} {
		v, ok := firstValue(families[metricName], container)
		if !ok {
			return sample, fmt.Errorf("cadvisor has no %s for container %q", metricName, c.cfg.Container)
		}
		sample.Values[signal] = v
	}
	return sample, nil
}

func containerStatus(pod *corev1.Pod, container string) (corev1.ContainerStatus, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == container {
			return status, true
		}
	}
	return corev1.ContainerStatus{}, false
}

func parse(raw []byte) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return families, nil
}

// firstValue returns the value of the first series in family whose labels
// include every label in match.
func firstValue(family *dto.MetricFamily, match map[string]string) (float64, bool) {
	if family == nil {
		return 0, false
	}
	for _, metric := range family.GetMetric() {
		if !hasLabels(metric, match) {
			continue
		}
		switch {
		case metric.GetGauge() != nil:
			return metric.GetGauge().GetValue(), true
		case metric.GetCounter() != nil:
			return metric.GetCounter().GetValue(), true
		case metric.GetUntyped() != nil:
			return metric.GetUntyped().GetValue(), true
		}
	}
	return 0, false
}

func hasLabels(metric *dto.Metric, match map[string]string) bool {
	for name, value := range match {
		found := false
		for _, label := range metric.GetLabel() {
			if label.GetName() == name && label.GetValue() == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
