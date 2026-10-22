package telemetry

import (
	"cmp"
	"context"
	"hash/fnv"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/utils/clock"

	nmaconfig "github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/scrape"
)

// Sender sends a report to the telemetry API.
type Sender interface {
	Send(ctx context.Context, r *nodetelemetry.Report) (nodetelemetry.Result, error)
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	// Interval is how often a report is sent.
	Interval time.Duration
	// NodeName spreads the first report time across nodes.
	// TODO: start with rendered NodeAttrs and use the NodeName from there
	NodeName string
	// Node is the report's resource attributes. If ComputeType is empty, it's
	// filled in from the runtime tags on each report.
	Node       nodetelemetry.NodeAttrs
	Collectors []scrape.Collector
	// ScrapeWait is how long each interval waits for the collectors.
	ScrapeWait time.Duration
	Sender     Sender
	// Registerer, if set, registers the manager's own metrics.
	Registerer prometheus.Registerer
	// Clock defaults to the real clock.
	Clock  clock.WithTicker
	Logger logr.Logger
	// Runtime defaults to the process's runtime context.
	Runtime *nmaconfig.RuntimeContext
}

// Manager collects from its collectors and sends a report every interval.
type Manager struct {
	cfg      ManagerConfig
	mu       sync.Mutex
	inFlight map[int]bool
	scrapes  *prometheus.CounterVec
	reports  *prometheus.CounterVec

	apiNotRegistered bool
}

// NewManager returns a Manager for cfg.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Clock == nil {
		cfg.Clock = clock.RealClock{}
	}
	if cfg.Runtime == nil {
		cfg.Runtime = nmaconfig.GetRuntimeContext()
	}
	m := &Manager{
		cfg:      cfg,
		inFlight: map[int]bool{},
		scrapes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nma_telemetry_scrape_total",
			Help: "Telemetry collector runs by component and result (ok, a not-reporting reason, skipped, late).",
		}, []string{"component", "result"}),
		reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nma_telemetry_report_total",
			Help: "Telemetry reports by outcome (accepted, dropped, hold, error).",
		}, []string{"outcome"}),
	}
	if cfg.Registerer != nil {
		cfg.Registerer.MustRegister(m.scrapes, m.reports)
	}
	return m
}

// Start sends reports until ctx is done, starting at FirstReportTime.
func (m *Manager) Start(ctx context.Context) error {
	clk := m.cfg.Clock
	prev := clk.Now()
	timer := clk.NewTimer(FirstReportTime(prev, m.cfg.Interval, m.cfg.NodeName).Sub(prev))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C():
	}
	ticker := clk.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	for {
		now := clk.Now()
		m.runInterval(ctx, prev, now)
		prev = now
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
		}
	}
}

// FirstReportTime returns the node's first report time: a fixed offset into
// the interval, derived from the node name so nodes don't all report at once.
func FirstReportTime(now time.Time, interval time.Duration, nodeName string) time.Time {
	h := fnv.New64a()
	_, _ = h.Write([]byte(nodeName))
	t := now.Truncate(interval).Add(time.Duration(h.Sum64() % uint64(interval)))
	if t.Before(now) {
		t = t.Add(interval)
	}
	return t
}

func (m *Manager) runInterval(ctx context.Context, start, end time.Time) {
	results := m.collect(ctx)
	node := m.cfg.Node
	tags := m.cfg.Runtime.Tags()
	if node.ComputeType == "" {
		for _, t := range []string{nmaconfig.EKSAuto, nmaconfig.Hybrid} {
			// the hybrid tag may be added with a delay, so we check for it on every
			// interval until the compute-type's been discovered
			if slices.Contains(tags, t) {
				node.ComputeType = t
				break
			}
		}
	}
	rep := nodetelemetry.NewReport(node, start, end)
	for _, r := range results {
		rep.AddScope(r.Scope)
	}

	res, err := m.cfg.Sender.Send(ctx, rep)
	if err != nil {
		m.cfg.Logger.Error(err, "telemetry report not sent")
		m.reports.WithLabelValues("error").Inc()
		return
	}
	m.reports.WithLabelValues(res.Outcome.String()).Inc()
	notRegistered := res.Status == http.StatusNotFound
	// only log first register/not-registered event to avoid log pollution
	if res.Status != 0 && notRegistered != m.apiNotRegistered {
		if notRegistered {
			m.cfg.Logger.Info("telemetry API is not registered, all reports will be dropped until it is", "status", res.Status, "message", res.Message)
		} else {
			m.cfg.Logger.Info("telemetry API is registered", "status", res.Status)
		}
		m.apiNotRegistered = notRegistered
	}
	if res.Outcome == nodetelemetry.Hold {
		m.cfg.Logger.V(2).Info("telemetry report held until the next interval", "status", res.Status, "message", res.Message)
		return
	}
	if res.Outcome == nodetelemetry.Dropped && !notRegistered {
		m.cfg.Logger.Info("telemetry report rejected", "status", res.Status, "message", res.Message)
	}
	// TODO: once reports are size-bounded, advance only the counters
	// that were sent.
	for _, r := range results {
		if r.Advance != nil {
			r.Advance()
		}
	}
}

func (m *Manager) collect(ctx context.Context) []scrape.Result {
	type done struct {
		i int
		r scrape.Result
	}
	ch := make(chan done, len(m.cfg.Collectors))
	launched := map[int]bool{}
	for i, c := range m.cfg.Collectors {
		m.mu.Lock()
		busy := m.inFlight[i]
		m.inFlight[i] = true
		m.mu.Unlock()
		if busy {
			m.scrapes.WithLabelValues(c.Scope(), "skipped").Inc()
			continue
		}
		launched[i] = true
		go func() {
			r := c.Scrape(ctx)
			m.mu.Lock()
			delete(m.inFlight, i)
			m.mu.Unlock()
			ch <- done{i, r}
		}()
	}

	got := map[int]scrape.Result{}
	timer := m.cfg.Clock.NewTimer(m.cfg.ScrapeWait)
	defer timer.Stop()
wait:
	for len(got) < len(launched) {
		select {
		case d := <-ch:
			got[d.i] = d.r
		case <-timer.C():
			break wait
		case <-ctx.Done():
			break wait
		}
	}

	var out []scrape.Result
	for i, c := range m.cfg.Collectors {
		r, ok := got[i]
		switch {
		case ok:
			m.scrapes.WithLabelValues(c.Scope(), cmp.Or(r.Failure, "ok")).Inc()
			out = append(out, r)
		case launched[i]:
			m.scrapes.WithLabelValues(c.Scope(), "late").Inc()
		}
	}
	return out
}

// BuildCollectors returns a collector for each target, and how long to wait for them each interval.
// A target without an endpoint is considered inprocess, and only gets a collector if it allowlists eks_problem;
// resources are attached to its problems.
func BuildCollectors(cfg *config.Config, source FatalSource, resources map[string]string, logger logr.Logger) ([]scrape.Collector, time.Duration) {
	var out []scrape.Collector
	var wait time.Duration
	for _, t := range cfg.Targets {
		if t.Endpoint == nil {
			if slices.ContainsFunc(t.Metrics, func(m config.MetricSpec) bool { return m.Name == nodetelemetry.MetricEksProblem }) {
				out = append(out, NewInProcessCollector(source, resources))
			}
			continue
		}
		s := cfg.Resolve(t)
		var identity string
		if t.Identity != nil {
			identity = t.Identity.RequireMetric
		}
		out = append(out, scrape.NewHTTPCollector(scrape.HTTPConfig{
			Scope:             t.Component,
			Endpoint:          *t.Endpoint,
			Path:              s.Path,
			Timeout:           s.Timeout,
			MaxBytes:          s.MaxBytes,
			Identity:          identity,
			Metrics:           resolveMetrics(t.Metrics, s.MaxSeries),
			NotReportingAfter: cfg.NotReportingThreshold(),
			OnceSeen:          s.Expected == config.ExpectedOnceSeen,
			Logger:            logger,
		}))
		wait = max(wait, s.Timeout)
	}
	return out, wait + time.Second
}

// resolveMetrics fills in each metric's MaxSeries from the target's resolved
// default, capped at the extension's per-metric point limit (each series that's
// sent is one point per report).
func resolveMetrics(specs []config.MetricSpec, targetMaxSeries int) []config.MetricSpec {
	out := make([]config.MetricSpec, 0, len(specs))
	for _, m := range specs {
		m.MaxSeries = min(cmp.Or(m.MaxSeries, targetMaxSeries), nodetelemetry.MaxPointsPerMetric)
		out = append(out, m)
	}
	return out
}
