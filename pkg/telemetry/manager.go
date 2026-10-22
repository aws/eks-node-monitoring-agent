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

// Values of the scrape metric's result label, besides not-reporting reasons.
const (
	scrapeResultOK      = "ok"
	scrapeResultSkipped = "skipped"
	scrapeResultLate    = "late"
)

// reportOutcomeError is the report metric's outcome when a report couldn't be
// sent at all.
const reportOutcomeError = "error"

// Sender sends a report to the telemetry API.
type Sender interface {
	Send(ctx context.Context, report *nodetelemetry.Report) (nodetelemetry.Result, error)
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
	manager := &Manager{
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
		cfg.Registerer.MustRegister(manager.scrapes, manager.reports)
	}
	return manager
}

// Start sends reports until ctx is done, starting at FirstReportTime.
func (manager *Manager) Start(ctx context.Context) error {
	wallClock := manager.cfg.Clock
	prev := wallClock.Now()
	timer := wallClock.NewTimer(FirstReportTime(prev, manager.cfg.Interval, manager.cfg.NodeName).Sub(prev))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C():
	}
	ticker := wallClock.NewTicker(manager.cfg.Interval)
	defer ticker.Stop()
	for {
		now := wallClock.Now()
		manager.runInterval(ctx, prev, now)
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
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(nodeName))
	first := now.Truncate(interval).Add(time.Duration(hash.Sum64() % uint64(interval)))
	if first.Before(now) {
		first = first.Add(interval)
	}
	return first
}

func (manager *Manager) runInterval(ctx context.Context, start, end time.Time) {
	results := manager.collect(ctx)
	node := manager.cfg.Node
	tags := manager.cfg.Runtime.Tags()
	if node.ComputeType == "" {
		for _, tag := range []string{nmaconfig.EKSAuto, nmaconfig.Hybrid} {
			// the hybrid tag may be added with a delay, so we check for it on every
			// interval until the compute-type's been discovered
			if slices.Contains(tags, tag) {
				node.ComputeType = tag
				break
			}
		}
	}
	report := nodetelemetry.NewReport(node, start, end)
	for _, result := range results {
		report.AddScope(result.Scope)
	}

	res, err := manager.cfg.Sender.Send(ctx, report)
	if err != nil {
		manager.cfg.Logger.Error(err, "telemetry report not sent")
		manager.reports.WithLabelValues(reportOutcomeError).Inc()
		return
	}
	manager.reports.WithLabelValues(string(res.Outcome)).Inc()
	notRegistered := res.Status == http.StatusNotFound
	// only log first register/not-registered event to avoid log pollution
	if res.Status != 0 && notRegistered != manager.apiNotRegistered {
		if notRegistered {
			manager.cfg.Logger.Info("telemetry API is not registered, all reports will be dropped until it is", "status", res.Status, "message", res.Message)
		} else {
			manager.cfg.Logger.Info("telemetry API is registered", "status", res.Status)
		}
		manager.apiNotRegistered = notRegistered
	}
	if res.Outcome == nodetelemetry.Hold {
		manager.cfg.Logger.V(2).Info("telemetry report held until the next interval", "status", res.Status, "message", res.Message)
		return
	}
	if res.Outcome == nodetelemetry.Dropped && !notRegistered {
		manager.cfg.Logger.Info("telemetry report rejected", "status", res.Status, "message", res.Message)
	}
	// TODO: once reports are size-bounded, advance only the counters
	// that were sent.
	for _, result := range results {
		if result.Advance != nil {
			result.Advance()
		}
	}
}

func (manager *Manager) collect(ctx context.Context) []scrape.Result {
	type done struct {
		index  int
		result scrape.Result
	}
	finished := make(chan done, len(manager.cfg.Collectors))
	launched := map[int]bool{}
	for index, collector := range manager.cfg.Collectors {
		manager.mu.Lock()
		busy := manager.inFlight[index]
		manager.inFlight[index] = true
		manager.mu.Unlock()
		if busy {
			manager.scrapes.WithLabelValues(collector.Scope(), scrapeResultSkipped).Inc()
			continue
		}
		launched[index] = true
		go func() {
			result := collector.Scrape(ctx)
			manager.mu.Lock()
			delete(manager.inFlight, index)
			manager.mu.Unlock()
			finished <- done{index, result}
		}()
	}

	got := map[int]scrape.Result{}
	timer := manager.cfg.Clock.NewTimer(manager.cfg.ScrapeWait)
	defer timer.Stop()
wait:
	for len(got) < len(launched) {
		select {
		case scraped := <-finished:
			got[scraped.index] = scraped.result
		case <-timer.C():
			break wait
		case <-ctx.Done():
			break wait
		}
	}

	var out []scrape.Result
	for index, collector := range manager.cfg.Collectors {
		result, ok := got[index]
		switch {
		case ok:
			manager.scrapes.WithLabelValues(collector.Scope(), cmp.Or(result.Failure, scrapeResultOK)).Inc()
			out = append(out, result)
		case launched[index]:
			manager.scrapes.WithLabelValues(collector.Scope(), scrapeResultLate).Inc()
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
	for _, target := range cfg.Targets {
		if target.Endpoint == nil {
			if slices.ContainsFunc(target.Metrics, func(spec config.MetricSpec) bool { return spec.Name == nodetelemetry.MetricEksProblem }) {
				out = append(out, NewInProcessCollector(source, resources))
			}
			continue
		}
		settings := cfg.Resolve(target)
		var identity string
		if target.Identity != nil {
			identity = target.Identity.RequireMetric
		}
		out = append(out, scrape.NewHTTPCollector(scrape.HTTPConfig{
			Scope:             target.Component,
			Endpoint:          *target.Endpoint,
			Path:              settings.Path,
			Timeout:           settings.Timeout,
			MaxBytes:          settings.MaxBytes,
			Identity:          identity,
			Metrics:           resolveMetrics(target.Metrics, settings.MaxSeries),
			NotReportingAfter: cfg.NotReportingThreshold(),
			OnceSeen:          settings.Expected == config.ExpectedOnceSeen,
			Logger:            logger,
		}))
		wait = max(wait, settings.Timeout)
	}
	return out, wait + time.Second
}

// resolveMetrics fills in each metric's MaxSeries from the target's resolved
// default, capped at the extension's per-metric point limit (each series that's
// sent is one point per report).
func resolveMetrics(specs []config.MetricSpec, targetMaxSeries int) []config.MetricSpec {
	out := make([]config.MetricSpec, 0, len(specs))
	for _, spec := range specs {
		spec.MaxSeries = min(cmp.Or(spec.MaxSeries, targetMaxSeries), nodetelemetry.MaxPointsPerMetric)
		out = append(out, spec)
	}
	return out
}
