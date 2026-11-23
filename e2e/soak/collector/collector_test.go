package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

const agentMetricsText = `# HELP go_goroutines Number of goroutines that currently exist.
# TYPE go_goroutines gauge
go_goroutines 87
# HELP go_memstats_heap_alloc_bytes Number of heap bytes allocated and currently in use.
# TYPE go_memstats_heap_alloc_bytes gauge
go_memstats_heap_alloc_bytes 1.2e+07
# HELP go_memstats_heap_inuse_bytes Number of heap bytes that are in use.
# TYPE go_memstats_heap_inuse_bytes gauge
go_memstats_heap_inuse_bytes 1.6e+07
# HELP go_memstats_sys_bytes Number of bytes obtained from system.
# TYPE go_memstats_sys_bytes gauge
go_memstats_sys_bytes 3.4e+07
# HELP process_open_fds Number of open file descriptors.
# TYPE process_open_fds gauge
process_open_fds 23
# HELP process_resident_memory_bytes Resident memory size in bytes.
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 4.1e+07
# HELP problem_condition_count unrelated agent metric
# TYPE problem_condition_count counter
problem_condition_count{reason="x",severity="Warning"} 3
`

const cadvisorText = `# HELP container_memory_working_set_bytes Current working set of the container in bytes
# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{container="",namespace="kube-system",pod="nma-a"} 9.9e+07
container_memory_working_set_bytes{container="eks-node-monitoring-agent-dcgm",namespace="kube-system",pod="nma-a"} 1e+06
container_memory_working_set_bytes{container="eks-node-monitoring-agent",namespace="kube-system",pod="nma-a"} 3.9e+07
container_memory_working_set_bytes{container="nginx",namespace="nma-soak",pod="web-1"} 5e+06
# HELP container_memory_rss Size of RSS in bytes.
# TYPE container_memory_rss gauge
container_memory_rss{container="",namespace="kube-system",pod="nma-a"} 3e+07
container_memory_rss{container="eks-node-monitoring-agent",namespace="kube-system",pod="nma-a"} 2.5e+07
`

type fakeFetcher struct {
	agent    map[string]string
	cadvisor map[string]string
	err      error
}

func (f fakeFetcher) AgentMetrics(_ context.Context, pod *corev1.Pod, port int) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if port != DefaultMetricsPort {
		return nil, errors.New("wrong port")
	}
	return []byte(f.agent[pod.Name]), nil
}

func (f fakeFetcher) CadvisorMetrics(_ context.Context, node string) ([]byte, error) {
	return []byte(f.cadvisor[node]), nil
}

func agentPod(name, node string, restarts int32, lastReason string) *corev1.Pod {
	status := corev1.ContainerStatus{Name: DefaultAgentContainer, RestartCount: restarts}
	if lastReason != "" {
		status.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: lastReason}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: DefaultAgentNamespace,
			Labels: map[string]string{"app.kubernetes.io/name": "eks-node-monitoring-agent"},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{status}},
	}
}

func node(name, pool string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{workload.PoolLabel: pool}}}
}

func TestCollect(t *testing.T) {
	client := fake.NewClientset(
		agentPod("nma-a", "node-a", 2, "OOMKilled"),
		node("node-a", workload.ChurnPool),
		// a pod that isn't scheduled yet is skipped, not an error.
		agentPod("nma-pending", "", 0, ""),
	)
	fetcher := fakeFetcher{
		agent:    map[string]string{"nma-a": agentMetricsText},
		cadvisor: map[string]string{"node-a": cadvisorText},
	}
	c := New(client, fetcher, DefaultConfig())
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	samples, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, samples, 1)
	s := samples[0]
	assert.Equal(t, now, s.Time)
	assert.Equal(t, "node-a", s.Node)
	assert.Equal(t, "nma-a", s.Pod)
	assert.Equal(t, workload.ChurnPool, s.Pool)
	assert.Equal(t, map[string]float64{
		HeapInuse:    1.6e7,
		HeapAlloc:    1.2e7,
		RuntimeSys:   3.4e7,
		RSS:          4.1e7,
		WorkingSet:   3.9e7, // the agent container only, not the pod total or dcgm.
		ContainerRSS: 2.5e7,
		Goroutines:   87,
		OpenFDs:      23,
		Restarts:     2,
		OOMKilled:    1,
	}, s.Values)
}

func TestCollectReportsFailingPodsAndKeepsOthers(t *testing.T) {
	client := fake.NewClientset(
		agentPod("nma-a", "node-a", 0, ""),
		agentPod("nma-b", "node-b", 0, ""),
		node("node-a", workload.SoakPool),
		node("node-b", workload.SoakPool),
	)
	fetcher := fakeFetcher{
		// nma-b's endpoint is missing the Go runtime metrics.
		agent:    map[string]string{"nma-a": agentMetricsText, "nma-b": "# TYPE up gauge\nup 1\n"},
		cadvisor: map[string]string{"node-a": cadvisorText, "node-b": cadvisorText},
	}
	samples, err := New(client, fetcher, DefaultConfig()).Collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nma-b")
	require.Len(t, samples, 1)
	assert.Equal(t, "nma-a", samples[0].Pod)
}

func TestCollectFetchError(t *testing.T) {
	client := fake.NewClientset(agentPod("nma-a", "node-a", 0, ""), node("node-a", workload.SoakPool))
	samples, err := New(client, fakeFetcher{err: errors.New("proxy refused")}, DefaultConfig()).Collect(context.Background())
	assert.ErrorContains(t, err, "proxy refused")
	assert.Empty(t, samples)
}
