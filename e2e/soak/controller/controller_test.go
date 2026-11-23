package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/analysis"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/churn"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
)

const (
	requestParam = "/nma-soak/beta/request"
	resultParam  = "/nma-soak/beta/result"
	image        = "602401143452.dkr.ecr.us-west-2.amazonaws.com/eks/eks-node-monitoring-agent:v1.7.4-eksbuild.1"
	mi           = 1 << 20
)

type fakeStore struct {
	values map[string]string
	puts   map[string][]string
	putErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string]string{}, puts: map[string][]string{}}
}

func (s *fakeStore) Get(_ context.Context, name string) (string, bool, error) {
	v, ok := s.values[name]
	return v, ok, nil
}

func (s *fakeStore) Put(_ context.Context, name, value string) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.values[name] = value
	s.puts[name] = append(s.puts[name], value)
	return nil
}

func (s *fakeStore) request(t *testing.T, req Request) {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	s.values[requestParam] = string(data)
}

func (s *fakeStore) result(t *testing.T) ReleaseResult {
	t.Helper()
	raw, ok := s.values[resultParam]
	require.True(t, ok, "no result written")
	var r ReleaseResult
	require.NoError(t, json.Unmarshal([]byte(raw), &r))
	return r
}

func (s *fakeStore) statuses(t *testing.T) []Status {
	t.Helper()
	var out []Status
	for _, raw := range s.puts[resultParam] {
		var r ReleaseResult
		require.NoError(t, json.Unmarshal([]byte(raw), &r))
		out = append(out, r.Status)
	}
	return out
}

// fakeSampler returns one sample per node, with values from fn.
type fakeSampler struct {
	now   func() time.Time
	nodes []string
	fn    func(elapsed time.Duration) map[string]float64
	start time.Time
	err   error
}

func (f *fakeSampler) Collect(context.Context) ([]collector.Sample, error) {
	var out []collector.Sample
	for _, node := range f.nodes {
		out = append(out, collector.Sample{Time: f.now(), Node: node, Pod: "nma-" + node, Pool: "soak", Values: f.fn(f.now().Sub(f.start))})
	}
	return out, f.err
}

type fakeMetrics struct {
	samples    int
	churn      []churn.Stats
	results    []analysis.Result
	heartbeats int
}

func (m *fakeMetrics) PublishSamples(_ context.Context, s []collector.Sample) error {
	m.samples += len(s)
	return nil
}

func (m *fakeMetrics) PublishChurn(_ context.Context, s churn.Stats, _ time.Time) error {
	m.churn = append(m.churn, s)
	return nil
}

func (m *fakeMetrics) PublishResult(_ context.Context, r analysis.Result, _ time.Time) error {
	m.results = append(m.results, r)
	return nil
}

func (m *fakeMetrics) PublishHeartbeat(context.Context, time.Time) error {
	m.heartbeats++
	return nil
}

type fakeRollout struct {
	done      bool
	lastImage string
}

func (r *fakeRollout) RolledOut(_ context.Context, imageURI string) (bool, string, error) {
	r.lastImage = imageURI
	if r.done {
		return true, "", nil
	}
	return false, "1 of 11 agent pods updated", nil
}

type fakeChurn struct{ stats churn.Stats }

func (f fakeChurn) Stats() churn.Stats { return f.stats }

func flat(time.Duration) map[string]float64 {
	return map[string]float64{
		collector.HeapInuse: 30 * mi, collector.HeapAlloc: 24 * mi, collector.RuntimeSys: 60 * mi,
		collector.RSS: 80 * mi, collector.WorkingSet: 82 * mi, collector.Goroutines: 100,
		collector.OpenFDs: 30, collector.Restarts: 0, collector.OOMKilled: 0,
	}
}

func heapLeak(elapsed time.Duration) map[string]float64 {
	v := flat(elapsed)
	v[collector.HeapInuse] += 4 * mi * elapsed.Hours()
	return v
}

type harness struct {
	t       *testing.T
	c       *Controller
	store   *fakeStore
	sampler *fakeSampler
	metrics *fakeMetrics
	rollout *fakeRollout
	now     time.Time
}

func newHarness(t *testing.T, recordOnly bool) *harness {
	t.Helper()
	th, err := analysis.DefaultThresholds()
	require.NoError(t, err)
	th.RecordOnly = recordOnly
	h := &harness{
		t: t, store: newFakeStore(), metrics: &fakeMetrics{}, rollout: &fakeRollout{},
		now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	}
	h.sampler = &fakeSampler{now: func() time.Time { return h.now }, nodes: []string{"node-a", "node-b"}, fn: flat, start: h.now}
	h.c = New(Config{
		RequestParameter: requestParam,
		ResultParameter:  resultParam,
		Window:           DefaultWindow,
		RolloutTimeout:   DefaultRolloutTimeout,
		Thresholds:       th,
	}, h.store, h.sampler, h.metrics, h.rollout, fakeChurn{churn.Stats{Created: 120}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.c.now = func() time.Time { return h.now }
	return h
}

// advance runs the controller's loops for d: Collect every 30s, Tick every minute.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	for end := h.now.Add(d); h.now.Before(end); {
		h.now = h.now.Add(DefaultCollectInterval)
		h.c.Collect(context.Background())
		if h.now.Second() == 0 {
			h.c.Tick(context.Background())
		}
	}
}

func TestReleaseRunsFullWindowAndPasses(t *testing.T) {
	h := newHarness(t, false)
	h.store.request(t, Request{ReleaseID: "evt-1", ReleaseVersion: "v1.7.4-eksbuild.1", ImageURI: image})
	require.NoError(t, h.c.Start(context.Background()))
	assert.Equal(t, StatusRollingOut, h.store.result(t).Status)

	h.advance(10 * time.Minute)
	assert.Equal(t, StatusRollingOut, h.store.result(t).Status, "the window waits for the rollout")
	assert.Equal(t, image, h.rollout.lastImage)

	h.rollout.done = true
	h.advance(time.Minute)
	r := h.store.result(t)
	require.Equal(t, StatusEvaluating, r.Status)
	require.NotNil(t, r.WindowStart)
	windowStart := *r.WindowStart

	h.advance(DefaultWindow - time.Minute)
	assert.Equal(t, StatusEvaluating, h.store.result(t).Status, "not finished before the window ends")
	h.advance(time.Minute)

	r = h.store.result(t)
	assert.Equal(t, StatusComplete, r.Status)
	assert.Equal(t, analysis.VerdictPass, r.Verdict)
	assert.True(t, r.Passed())
	assert.Equal(t, "evt-1", r.ReleaseID)
	assert.Equal(t, image, r.ImageURI)
	require.NotNil(t, r.Analysis)
	assert.Equal(t, 2, r.Analysis.Nodes)
	assert.Equal(t, windowStart, r.Analysis.WindowStart)
	assert.Equal(t, DefaultWindow, r.WindowEnd.Sub(windowStart))
	assert.Equal(t, []Status{StatusRollingOut, StatusEvaluating, StatusComplete}, h.store.statuses(t))
	require.Len(t, h.metrics.results, 1, "the window result is published for alarms")
}

func TestLeakFailsRelease(t *testing.T) {
	h := newHarness(t, false)
	h.rollout.done = true
	h.store.request(t, Request{ReleaseID: "evt-1", ImageURI: image})
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(time.Minute)
	h.sampler.start = h.now
	h.sampler.fn = heapLeak
	h.advance(DefaultWindow)

	r := h.store.result(t)
	assert.Equal(t, StatusComplete, r.Status)
	assert.Equal(t, analysis.VerdictFail, r.Verdict)
	assert.False(t, r.Passed())
	assert.True(t, r.Analysis.Has(analysis.CheckSlope))
}

func TestRecordOnlyLeakStillPasses(t *testing.T) {
	h := newHarness(t, true)
	h.rollout.done = true
	h.store.request(t, Request{ReleaseID: "evt-1"})
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(time.Minute)
	h.sampler.start = h.now
	h.sampler.fn = heapLeak
	h.advance(DefaultWindow)

	r := h.store.result(t)
	assert.Equal(t, analysis.VerdictRecordOnly, r.Verdict)
	assert.True(t, r.Passed(), "record-only reports findings without blocking the release")
	assert.NotEmpty(t, r.Analysis.Findings)
}

func TestRolloutTimeout(t *testing.T) {
	h := newHarness(t, false)
	h.store.request(t, Request{ReleaseID: "evt-1", ImageURI: image})
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(DefaultRolloutTimeout)

	r := h.store.result(t)
	assert.Equal(t, StatusRolloutTimeout, r.Status)
	assert.Equal(t, analysis.VerdictFail, r.Verdict)
	assert.False(t, r.Passed())
	assert.Contains(t, r.Detail, "1 of 11 agent pods updated")

	h.rollout.done = true
	h.advance(time.Hour)
	assert.Equal(t, StatusRolloutTimeout, h.store.result(t).Status, "a timed-out release isn't retried")
}

func TestHandledReleaseIsNotRerunAfterRestart(t *testing.T) {
	h := newHarness(t, false)
	h.store.request(t, Request{ReleaseID: "evt-1"})
	prev, err := json.Marshal(ReleaseResult{Request: Request{ReleaseID: "evt-1"}, Status: StatusComplete, Verdict: analysis.VerdictPass})
	require.NoError(t, err)
	h.store.values[resultParam] = string(prev)

	require.NoError(t, h.c.Start(context.Background()))
	h.advance(time.Hour)
	assert.Empty(t, h.store.puts[resultParam], "the stored result must not be overwritten")
}

func TestUnfinishedReleaseRestartsAfterRestart(t *testing.T) {
	h := newHarness(t, false)
	h.store.request(t, Request{ReleaseID: "evt-1"})
	prev, err := json.Marshal(ReleaseResult{Request: Request{ReleaseID: "evt-1"}, Status: StatusEvaluating})
	require.NoError(t, err)
	h.store.values[resultParam] = string(prev)

	require.NoError(t, h.c.Start(context.Background()))
	assert.Equal(t, StatusRollingOut, h.store.result(t).Status, "the window starts over since its samples were lost")
}

func TestNewReleaseSupersedesInFlightRelease(t *testing.T) {
	h := newHarness(t, false)
	h.rollout.done = true
	h.store.request(t, Request{ReleaseID: "evt-1"})
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(time.Hour)

	h.store.request(t, Request{ReleaseID: "evt-2"})
	h.advance(time.Minute)
	assert.Equal(t, []Status{StatusRollingOut, StatusEvaluating, StatusSuperseded, StatusRollingOut, StatusEvaluating}, h.store.statuses(t))
	r := h.store.result(t)
	assert.Equal(t, "evt-2", r.ReleaseID)
	assert.Equal(t, StatusEvaluating, r.Status)
}

func TestRollingWindowsBetweenReleases(t *testing.T) {
	h := newHarness(t, false)
	require.NoError(t, h.c.Start(context.Background()))
	h.sampler.fn = heapLeak
	h.advance(2 * DefaultWindow)

	assert.Empty(t, h.store.puts[resultParam], "rolling windows don't write a release result")
	require.Len(t, h.metrics.results, 2, "each rolling window publishes its result so leaks still alarm")
	for _, r := range h.metrics.results {
		assert.Equal(t, analysis.VerdictFail, r.Verdict)
	}
}

func TestWindowOnlyKeepsItsOwnSamples(t *testing.T) {
	h := newHarness(t, false)
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(DefaultWindow + time.Hour)
	assert.LessOrEqual(t, len(h.c.samples), 2*int(time.Hour/DefaultCollectInterval)+2, "samples are dropped when a window ends")
}

func TestTickPublishesHeartbeatAndChurn(t *testing.T) {
	h := newHarness(t, false)
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(5 * time.Minute)
	assert.Equal(t, 5, h.metrics.heartbeats)
	assert.Len(t, h.metrics.churn, 5)
	assert.Equal(t, 5*2*2, h.metrics.samples, "two nodes sampled every 30s")
}

func TestHeartbeatContinuesWhenSamplingFails(t *testing.T) {
	h := newHarness(t, false)
	h.sampler.err = errors.New("proxy refused")
	h.sampler.nodes = nil
	require.NoError(t, h.c.Start(context.Background()))
	h.advance(3 * time.Minute)
	assert.Equal(t, 3, h.metrics.heartbeats)
	assert.Zero(t, h.metrics.samples)
}
