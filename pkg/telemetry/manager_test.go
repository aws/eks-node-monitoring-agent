package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/scrape"
)

type countingSender struct {
	mu sync.Mutex
	n  int
}

func (s *countingSender) Send(context.Context, *nodetelemetry.Report) (nodetelemetry.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return nodetelemetry.Result{Outcome: nodetelemetry.Accepted}, nil
}

func (s *countingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func TestFirstReportTime(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := FirstReportTime(now, 5*time.Minute, "node-a")
	assert.False(t, a.Before(now))
	assert.Less(t, a.Sub(now), 5*time.Minute)
	assert.NotEqual(t, a, FirstReportTime(now, 5*time.Minute, "node-b"), "nodes are spread out")
	assert.Zero(t, FirstReportTime(now.Add(7*time.Minute), 5*time.Minute, "node-a").Sub(a)%(5*time.Minute), "the offset doesn't depend on start time")
}

func TestManager_Schedule(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(1_000_000, 0))
	sender := &countingSender{}
	m := NewManager(ManagerConfig{Interval: 5 * time.Minute, NodeName: "node-a", Sender: sender, Clock: clk, ScrapeWait: time.Second, Logger: logr.Discard()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Start(ctx) }()

	first := FirstReportTime(clk.Now(), 5*time.Minute, "node-a")
	require.Eventually(t, clk.HasWaiters, time.Second, time.Millisecond)
	clk.SetTime(first.Add(-time.Second))
	assert.Zero(t, sender.count())
	clk.SetTime(first)
	require.Eventually(t, func() bool { return sender.count() == 1 }, time.Second, time.Millisecond)
	require.Eventually(t, clk.HasWaiters, time.Second, time.Millisecond)
	clk.Step(5 * time.Minute)
	require.Eventually(t, func() bool { return sender.count() == 2 }, time.Second, time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

type hung struct{ calls, release chan struct{} }

func (h hung) Scope() string { return "slow" }
func (h hung) Scrape(ctx context.Context) scrape.Result {
	h.calls <- struct{}{}
	<-h.release
	return scrape.Result{Scope: nodetelemetry.Scope{Name: "slow"}}
}

func TestManager_HungCollector(t *testing.T) {
	h := hung{calls: make(chan struct{}, 10), release: make(chan struct{})}
	defer close(h.release)
	sender := &countingSender{}
	m := NewManager(ManagerConfig{Collectors: []scrape.Collector{h}, ScrapeWait: 20 * time.Millisecond, Sender: sender, Logger: logr.Discard()})
	m.runInterval(context.Background(), time.Unix(0, 0), time.Unix(60, 0))
	m.runInterval(context.Background(), time.Unix(60, 0), time.Unix(120, 0))
	assert.Equal(t, 2, sender.count())
	assert.Len(t, h.calls, 1)
}
