package scrape

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

// acceptHeader asks for delimited protobuf, falling back to the text format.
const acceptHeader = "application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited;q=0.7,text/plain;version=0.0.4;q=0.3"

// HTTPConfig configures an HTTPCollector for one target.
type HTTPConfig struct {
	Scope    string
	Endpoint config.TargetEndpoint
	Path     string
	Timeout  time.Duration
	MaxBytes int64
	// Identity, if set, is a metric every response must contain.
	Identity string
	// Metrics is the allowlist, with MaxSeries already resolved.
	Metrics []config.MetricSpec
	// NotReportingAfter is how many failed scrapes in a row it takes to report
	// the target as not reporting.
	NotReportingAfter int
	// OnceSeen ignores failed scrapes until the first successful one.
	OnceSeen bool
	// Clock defaults to time.Now.
	Clock  func() time.Time
	Logger logr.Logger
}

// HTTPCollector scrapes one target's Prometheus metrics endpoint over plain
// HTTP. Each scrape is a GET to http://<Endpoint.Address>:<Endpoint.Port><Path>
// whose Accept header asks for the Prometheus exposition format, either
// delimited protobuf (io.prometheus.client.MetricFamily) or text (version
// 0.0.4). The response is parsed according to its Content-Type, and as text
// if the Content-Type isn't a Prometheus format. Other formats, such as JSON,
// fail to parse.
//
// A scrape fails if the target can't be reached or times out, answers with a
// status of 300 or above (redirects aren't followed), returns more than
// MaxBytes, can't be parsed, or doesn't expose the Identity metric. Otherwise
// only allowlisted Metrics are kept: counters become deltas against their
// baselines, and gauges are combined across series that share their kept
// labels.
type HTTPCollector struct {
	cfg      HTTPConfig
	allow    map[string]config.MetricSpec
	url      string
	client   *http.Client
	failures int
	seen     bool
	// lastErrorStatus is the HTTP status of the last failed fetch, so an
	// error status is logged once rather than on every scrape.
	lastErrorStatus int

	mu        sync.Mutex
	baselines *Baselines
}

// NewHTTPCollector returns an HTTPCollector for cfg.
func NewHTTPCollector(cfg HTTPConfig) *HTTPCollector {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	allow := map[string]config.MetricSpec{}
	for _, spec := range cfg.Metrics {
		allow[spec.Name] = spec
	}
	return &HTTPCollector{
		cfg:   cfg,
		allow: allow,
		url:   "http://" + net.JoinHostPort(cfg.Endpoint.Address, strconv.Itoa(cfg.Endpoint.Port)) + cfg.Path,
		client: &http.Client{
			Timeout:       cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baselines: NewBaselines(),
	}
}

// Scope returns the target's component name.
func (collector *HTTPCollector) Scope() string { return collector.cfg.Scope }

// Scrape scrapes the target once. Only one scrape should run at a time.
func (collector *HTTPCollector) Scrape(ctx context.Context) Result {
	now := collector.cfg.Clock()
	parsed, reason := collector.fetch(ctx)
	if reason == "" && collector.cfg.Identity != "" && !parsed.SawIdentity {
		reason = ReasonIdentityMismatch
	}
	if reason != "" {
		return collector.failed(reason)
	}
	collector.failures, collector.seen = 0, true

	res := Result{Scope: nodetelemetry.Scope{Name: collector.cfg.Scope, Problems: parsed.Problems}}
	counters := map[string][]Series{}
	for name, series := range parsed.Series {
		spec := collector.allow[name]
		if spec.Type == config.MetricTypeCounter {
			counters[name] = series
			continue
		}
		// TODO: constrain to accepted config fields after adding config validation
		res.Metrics = append(res.Metrics, nodetelemetry.Metric{Name: name, Unit: spec.Unit, Gauges: gauges(series, spec)})
	}
	collector.mu.Lock()
	deltas := collector.baselines.Observe(now, counters)
	collector.mu.Unlock()
	for name, sums := range sums(deltas, collector.allow) {
		res.Metrics = append(res.Metrics, nodetelemetry.Metric{Name: name, Unit: collector.allow[name].Unit, Sums: sums})
	}
	sort.Slice(res.Metrics, func(left, right int) bool { return res.Metrics[left].Name < res.Metrics[right].Name })
	res.Advance = func() {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		collector.baselines.Advance()
	}
	return res
}

func (collector *HTTPCollector) fetch(ctx context.Context) (Parsed, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, collector.url, nil)
	if err != nil {
		return Parsed{}, ReasonParseError
	}
	req.Header.Set("Accept", acceptHeader)
	resp, err := collector.client.Do(req)
	if err != nil {
		var netError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netError) && netError.Timeout()) {
			return Parsed{}, ReasonTimeout
		}
		return Parsed{}, ReasonConnectionRefused
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode != collector.lastErrorStatus {
			collector.cfg.Logger.Info("scrape target returned a new error status", "component", collector.cfg.Scope, "url", collector.url, "status", resp.StatusCode)
			collector.lastErrorStatus = resp.StatusCode
		}
		return Parsed{}, ReasonConnectionRefused
	}
	collector.lastErrorStatus = 0
	parsed, err := Parse(resp.Header.Get("Content-Type"), resp.Body, collector.allow, collector.cfg.MaxBytes, collector.cfg.Identity)
	switch {
	case errors.Is(err, errTooLarge):
		return Parsed{}, ReasonResponseTooLarge
	case err != nil:
		return Parsed{}, ReasonParseError
	}
	return parsed, ""
}

func (collector *HTTPCollector) failed(reason string) Result {
	collector.mu.Lock()
	collector.baselines.ScrapeFailed()
	collector.mu.Unlock()
	res := Result{Scope: nodetelemetry.Scope{Name: collector.cfg.Scope}, Failure: reason}
	if collector.cfg.OnceSeen && !collector.seen {
		return res
	}
	if collector.failures++; collector.failures >= collector.cfg.NotReportingAfter {
		res.NotReporting = reason
	}
	return res
}

func gauges(series []Series, spec config.MetricSpec) []nodetelemetry.GaugePoint {
	byKey := map[string]*nodetelemetry.GaugePoint{}
	for _, sample := range series {
		attrs := kept(sample.Labels, spec.Labels)
		key := seriesKey("", attrs)
		point, ok := byKey[key]
		switch {
		case !ok:
			byKey[key] = &nodetelemetry.GaugePoint{Value: sample.Value, Attrs: attrs}
		case spec.Aggregate == config.AggregateMax:
			point.Value = max(point.Value, sample.Value)
		case spec.Aggregate == config.AggregateMin:
			point.Value = min(point.Value, sample.Value)
		default:
			point.Value += sample.Value
		}
	}
	out := make([]nodetelemetry.GaugePoint, 0, len(byKey))
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		out = append(out, *byKey[key])
	}
	return out
}

func sums(deltas []Delta, allow map[string]config.MetricSpec) map[string][]nodetelemetry.SumPoint {
	byKey := map[string]map[string]*nodetelemetry.SumPoint{}
	for _, delta := range deltas {
		attrs := kept(delta.Series.Labels, allow[delta.Metric].Labels)
		key := seriesKey("", attrs)
		if byKey[delta.Metric] == nil {
			byKey[delta.Metric] = map[string]*nodetelemetry.SumPoint{}
		}
		point, ok := byKey[delta.Metric][key]
		if !ok {
			point = &nodetelemetry.SumPoint{Attrs: attrs, StartTime: delta.Start}
			byKey[delta.Metric][key] = point
		}
		point.Delta += delta.Value
		if delta.Start.Before(point.StartTime) {
			point.StartTime = delta.Start
		}
	}
	out := map[string][]nodetelemetry.SumPoint{}
	for metric, points := range byKey {
		for _, key := range slices.Sorted(maps.Keys(points)) {
			out[metric] = append(out[metric], *points[key])
		}
	}
	return out
}

func kept(labels map[string]string, keep []string) nodetelemetry.Attrs {
	out := nodetelemetry.Attrs{}
	for _, labelName := range keep {
		if labelValue, ok := labels[labelName]; ok {
			out[labelName] = labelValue
		}
	}
	return out
}

var _ Collector = (*HTTPCollector)(nil)
