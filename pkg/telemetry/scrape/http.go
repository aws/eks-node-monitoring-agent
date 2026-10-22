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
	for _, m := range cfg.Metrics {
		allow[m.Name] = m
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
func (c *HTTPCollector) Scope() string { return c.cfg.Scope }

// Scrape scrapes the target once. Only one scrape should run at a time.
func (c *HTTPCollector) Scrape(ctx context.Context) Result {
	now := c.cfg.Clock()
	parsed, reason := c.fetch(ctx)
	if reason == "" && c.cfg.Identity != "" && !parsed.SawIdentity {
		reason = ReasonIdentityMismatch
	}
	if reason != "" {
		return c.failed(reason)
	}
	c.failures, c.seen = 0, true

	res := Result{Scope: nodetelemetry.Scope{Name: c.cfg.Scope, Problems: parsed.Problems}}
	counters := map[string][]Series{}
	for name, series := range parsed.Series {
		spec := c.allow[name]
		if spec.Type == config.MetricTypeCounter {
			counters[name] = series
			continue
		}
		// TODO: constrain to accepted config fields after adding config validation
		res.Metrics = append(res.Metrics, nodetelemetry.Metric{Name: name, Unit: spec.Unit, Gauges: gauges(series, spec)})
	}
	c.mu.Lock()
	deltas := c.baselines.Observe(now, counters)
	c.mu.Unlock()
	for name, sums := range sums(deltas, c.allow) {
		res.Metrics = append(res.Metrics, nodetelemetry.Metric{Name: name, Unit: c.allow[name].Unit, Sums: sums})
	}
	sort.Slice(res.Metrics, func(i, j int) bool { return res.Metrics[i].Name < res.Metrics[j].Name })
	res.Advance = func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.baselines.Advance()
	}
	return res
}

func (c *HTTPCollector) fetch(ctx context.Context) (Parsed, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Parsed{}, ReasonParseError
	}
	req.Header.Set("Accept", "application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited;q=0.7,text/plain;version=0.0.4;q=0.3")
	resp, err := c.client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return Parsed{}, ReasonTimeout
		}
		return Parsed{}, ReasonConnectionRefused
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		if resp.StatusCode != c.lastErrorStatus {
			c.cfg.Logger.Info("scrape target returned a new error status", "component", c.cfg.Scope, "url", c.url, "status", resp.StatusCode)
			c.lastErrorStatus = resp.StatusCode
		}
		return Parsed{}, ReasonConnectionRefused
	}
	c.lastErrorStatus = 0
	parsed, err := Parse(resp.Header.Get("Content-Type"), resp.Body, c.allow, c.cfg.MaxBytes, c.cfg.Identity)
	switch {
	case errors.Is(err, errTooLarge):
		return Parsed{}, ReasonResponseTooLarge
	case err != nil:
		return Parsed{}, ReasonParseError
	}
	return parsed, ""
}

func (c *HTTPCollector) failed(reason string) Result {
	c.mu.Lock()
	c.baselines.ScrapeFailed()
	c.mu.Unlock()
	res := Result{Scope: nodetelemetry.Scope{Name: c.cfg.Scope}, Failure: reason}
	if c.cfg.OnceSeen && !c.seen {
		return res
	}
	if c.failures++; c.failures >= c.cfg.NotReportingAfter {
		res.NotReporting = reason
	}
	return res
}

func gauges(series []Series, spec config.MetricSpec) []nodetelemetry.GaugePoint {
	byKey := map[string]*nodetelemetry.GaugePoint{}
	for _, s := range series {
		attrs := kept(s.Labels, spec.Labels)
		key := seriesKey("", attrs)
		p, ok := byKey[key]
		switch {
		case !ok:
			byKey[key] = &nodetelemetry.GaugePoint{Value: s.Value, Attrs: attrs}
		case spec.Aggregate == "max":
			p.Value = max(p.Value, s.Value)
		case spec.Aggregate == "min":
			p.Value = min(p.Value, s.Value)
		default:
			p.Value += s.Value
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
	for _, d := range deltas {
		attrs := kept(d.Series.Labels, allow[d.Metric].Labels)
		key := seriesKey("", attrs)
		if byKey[d.Metric] == nil {
			byKey[d.Metric] = map[string]*nodetelemetry.SumPoint{}
		}
		p, ok := byKey[d.Metric][key]
		if !ok {
			p = &nodetelemetry.SumPoint{Attrs: attrs, StartTime: d.Start}
			byKey[d.Metric][key] = p
		}
		p.Delta += d.Value
		if d.Start.Before(p.StartTime) {
			p.StartTime = d.Start
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
	for _, k := range keep {
		if v, ok := labels[k]; ok {
			out[k] = v
		}
	}
	return out
}

var _ Collector = (*HTTPCollector)(nil)
