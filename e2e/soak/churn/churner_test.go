package churn

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

const testNamespace = "nma-soak"

func testTemplates() []corev1.Pod {
	return []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pause", Labels: map[string]string{workload.FlavorLabel: "pause"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "pause"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "writer", Labels: map[string]string{workload.FlavorLabel: "writer"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "writer", Image: "al2023"}}},
		},
	}
}

func newTestChurner(t *testing.T, mutate func(*Config), objs ...*corev1.Pod) (*Churner, *fake.Clientset, *time.Time) {
	t.Helper()
	client := fake.NewClientset()
	for _, obj := range objs {
		_, err := client.CoreV1().Pods(obj.Namespace).Create(context.Background(), obj, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	cfg := DefaultConfig(testNamespace, testTemplates())
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(client, cfg)
	require.NoError(t, err)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, client, &now
}

func listPods(t *testing.T, client *fake.Clientset) []corev1.Pod {
	t.Helper()
	list, err := client.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	return list.Items
}

func TestDefaultRate(t *testing.T) {
	c, _, _ := newTestChurner(t, nil)
	assert.Equal(t, rate.Limit(2), c.limiter.Limit(), "120 pods/min is 2 pods/s")
	assert.Equal(t, 1, c.limiter.Burst(), "creations should be spread evenly, not burst")
}

func TestCreateOneRoundRobin(t *testing.T) {
	c, client, _ := newTestChurner(t, nil)
	for range 4 {
		c.createOne(context.Background())
	}

	pods := listPods(t, client)
	require.Len(t, pods, 4)
	flavors := map[string]int{}
	for _, pod := range pods {
		flavors[pod.Labels[workload.FlavorLabel]]++
		assert.Equal(t, testNamespace, pod.Namespace)
		assert.Equal(t, workload.ChurnComponent, pod.Labels[workload.ComponentLabel], "the NetworkPolicy selects on this label")
		assert.Equal(t, workload.PartOfValue, pod.Labels[workload.PartOfLabel])
		assert.Contains(t, pod.Name, "churn-"+pod.Labels[workload.FlavorLabel]+"-")
	}
	assert.Equal(t, map[string]int{"pause": 2, "writer": 2}, flavors, "flavors should alternate")
	assert.Empty(t, testTemplates()[0].Namespace, "templates must not be mutated")
	assert.EqualValues(t, 4, c.Stats().Created)
}

func TestCreateOneRespectsMaxInFlight(t *testing.T) {
	c, client, _ := newTestChurner(t, func(cfg *Config) { cfg.MaxInFlight = 2 })
	for range 3 {
		c.createOne(context.Background())
	}
	assert.Len(t, listPods(t, client), 2)
	stats := c.Stats()
	assert.EqualValues(t, 2, stats.Created)
	assert.EqualValues(t, 1, stats.SkippedAtCapacity)
	assert.Equal(t, 2, stats.InFlight)
}

func TestReapDeletesPodsAtLifetime(t *testing.T) {
	c, client, now := newTestChurner(t, nil)
	ctx := context.Background()
	c.createOne(ctx)
	c.createOne(ctx)

	*now = now.Add(DefaultLifetime - time.Second)
	require.NoError(t, c.reap(ctx))
	assert.Len(t, listPods(t, client), 2, "pods younger than their lifetime are kept")

	*now = now.Add(time.Second)
	require.NoError(t, c.reap(ctx))
	assert.Empty(t, listPods(t, client), "pods at their lifetime are deleted")
	stats := c.Stats()
	assert.EqualValues(t, 2, stats.Deleted)
	assert.EqualValues(t, 2, stats.NeverReady, "these pods never became Ready")
	assert.Zero(t, stats.InFlight)
}

func TestReapAdoptsLeftoverPods(t *testing.T) {
	created := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	leftover := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:              "churn-pause-leftover",
		Namespace:         testNamespace,
		CreationTimestamp: metav1.NewTime(created),
		Labels: map[string]string{
			workload.PartOfLabel:    workload.PartOfValue,
			workload.ComponentLabel: workload.ChurnComponent,
		},
	}}
	unrelated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:              "background",
		Namespace:         testNamespace,
		CreationTimestamp: metav1.NewTime(created),
	}}
	c, client, _ := newTestChurner(t, nil, leftover, unrelated)

	require.NoError(t, c.reap(context.Background()))
	pods := listPods(t, client)
	require.Len(t, pods, 1, "the leftover churn pod is past its lifetime and deleted")
	assert.Equal(t, "background", pods[0].Name, "non-churn pods are never touched")
}

func TestReapRecordsStartLatencyAndFailures(t *testing.T) {
	created := time.Date(2026, 10, 22, 11, 59, 30, 0, time.UTC)
	labels := map[string]string{
		workload.PartOfLabel:    workload.PartOfValue,
		workload.ComponentLabel: workload.ChurnComponent,
	}
	ready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "churn-ready", Namespace: testNamespace, CreationTimestamp: metav1.NewTime(created), Labels: labels},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(created.Add(3 * time.Second)),
			}},
		},
	}
	failed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "churn-failed", Namespace: testNamespace, CreationTimestamp: metav1.NewTime(created), Labels: labels},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	c, client, _ := newTestChurner(t, nil, ready, failed)

	require.NoError(t, c.reap(context.Background()))
	pods := listPods(t, client)
	require.Len(t, pods, 1, "failed pods are deleted right away")
	assert.Equal(t, "churn-ready", pods[0].Name)

	stats := c.Stats()
	assert.Equal(t, []time.Duration{3 * time.Second}, stats.StartLatencies)
	assert.EqualValues(t, 1, stats.Failed)
	assert.Empty(t, c.Stats().StartLatencies, "start latencies are drained by Stats")

	require.NoError(t, c.reap(context.Background()))
	assert.Empty(t, c.Stats().StartLatencies, "a pod's start latency is only recorded once")
}

func TestRunChurnsUntilCancelled(t *testing.T) {
	c, client, _ := newTestChurner(t, func(cfg *Config) {
		cfg.PodsPerMinute = 60 * 100
		cfg.ReapInterval = 10 * time.Millisecond
	})
	c.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	require.NoError(t, c.Run(ctx))
	stats := c.Stats()
	assert.Positive(t, stats.Created)
	assert.Len(t, listPods(t, client), stats.InFlight)
}
