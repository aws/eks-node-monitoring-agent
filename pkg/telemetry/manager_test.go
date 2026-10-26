package telemetry

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
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
	m := NewManager(ManagerConfig{Interval: 5 * time.Minute, Node: nodetelemetry.NodeAttrs{NodeName: "node-a"}, Sender: sender, Clock: clk, ScrapeWait: time.Second, Logger: logr.Discard()})
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

type scriptedSender struct{ results []nodetelemetry.Result }

func (s *scriptedSender) Send(context.Context, *nodetelemetry.Report) (nodetelemetry.Result, error) {
	r := s.results[0]
	s.results = s.results[1:]
	return r, nil
}

func TestManager_LogsAPIRegistrationChanges(t *testing.T) {
	notFound := nodetelemetry.Result{Outcome: nodetelemetry.Dropped, Status: http.StatusNotFound}
	accepted := nodetelemetry.Result{Outcome: nodetelemetry.Accepted, Status: http.StatusOK}
	transport := nodetelemetry.Result{Outcome: nodetelemetry.Hold}
	var logs []string
	m := NewManager(ManagerConfig{
		Sender: &scriptedSender{results: []nodetelemetry.Result{notFound, notFound, transport, notFound, accepted, accepted, notFound}},
		Logger: funcr.New(func(_, args string) { logs = append(logs, args) }, funcr.Options{}),
	})
	for i := 0; i < 7; i++ {
		m.runInterval(context.Background(), time.Unix(int64(i)*60, 0), time.Unix(int64(i+1)*60, 0))
	}
	var msgs []string
	for _, l := range logs {
		for _, want := range []string{"telemetry API is not registered", "telemetry API is registered", "telemetry report rejected"} {
			if strings.Contains(l, `"msg"="`+want) {
				msgs = append(msgs, want)
			}
		}
	}
	assert.Equal(t, []string{
		"telemetry API is not registered",
		"telemetry API is registered",
		"telemetry API is not registered",
	}, msgs, "logged once when it goes missing and once when it comes back; a transport error changes nothing")
}

func TestResolveMetrics_CapsMaxSeriesAtTheAPILimit(t *testing.T) {
	got := map[string]int{}
	for _, m := range resolveMetrics([]config.MetricSpec{
		{Name: "small", MaxSeries: 10},
		{Name: "huge", MaxSeries: 500},
		{Name: "default"},
	}, config.DefaultMaxSeries) {
		got[m.Name] = m.MaxSeries
	}
	assert.Equal(t, map[string]int{"small": 10, "huge": nodetelemetry.MaxPointsPerMetric, "default": config.DefaultMaxSeries}, got)
	assert.Equal(t, nodetelemetry.MaxPointsPerMetric, resolveMetrics([]config.MetricSpec{{Name: "m"}}, 200)[0].MaxSeries, "a target default above the limit is capped too")
}
