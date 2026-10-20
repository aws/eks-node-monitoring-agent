package scrape

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

type target struct {
	mu   sync.Mutex
	body string
}

func (tg *target) set(body string) { tg.mu.Lock(); tg.body = body; tg.mu.Unlock() }

func newCollector(t *testing.T, tg *target, mutate func(*HTTPConfig)) *HTTPCollector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		if tg.body == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", textCT)
		_, _ = w.Write([]byte(tg.body))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	cfg := HTTPConfig{
		Scope:    "ipamd",
		Endpoint: config.TargetEndpoint{Address: host, Port: p},
		Path:     "/metrics",
		Timeout:  time.Second,
		MaxBytes: 1 << 20,
		Metrics: []config.MetricSpec{
			{Name: "depth", Type: config.MetricTypeGauge, Unit: "1", Labels: []string{"api"}, Aggregate: "max", MaxSeries: 50},
			{Name: "errors_total", Type: config.MetricTypeCounter, Unit: "1", Labels: []string{"api"}, MaxSeries: 50},
		},
		NotReportingAfter: 2,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewHTTPCollector(cfg)
}

func metric(res Result, name string) nodetelemetry.Metric {
	for _, m := range res.Metrics {
		if m.Name == name {
			return m
		}
	}
	return nodetelemetry.Metric{}
}

func TestHTTPCollector_AggregatesByKeptLabels(t *testing.T) {
	tg := &target{}
	c := newCollector(t, tg, nil)
	body := func(a1, a2 int) string {
		return "# TYPE depth gauge\ndepth{api=\"A\",pod=\"x\"} 3\ndepth{api=\"A\",pod=\"y\"} 7\n" +
			"# TYPE errors_total counter\nerrors_total{api=\"A\",error=\"e1\"} " + strconv.Itoa(a1) + "\nerrors_total{api=\"A\",error=\"e2\"} " + strconv.Itoa(a2) + "\n"
	}

	tg.set(body(10, 10))
	res := c.Scrape(context.Background())
	assert.Equal(t, []nodetelemetry.GaugePoint{{Value: 7, Attrs: nodetelemetry.Attrs{"api": "A"}}}, metric(res, "depth").Gauges)
	assert.Empty(t, metric(res, "errors_total").Sums, "no baselines yet")

	tg.set(body(12, 13))
	res = c.Scrape(context.Background())
	assert.Equal(t, 5.0, metric(res, "errors_total").Sums[0].Delta)

	tg.set(body(14, 13))
	res = c.Scrape(context.Background())
	assert.Equal(t, 7.0, metric(res, "errors_total").Sums[0].Delta, "not advanced: covers both intervals")
	res.Advance()

	tg.set(body(15, 13))
	res = c.Scrape(context.Background())
	assert.Equal(t, 1.0, metric(res, "errors_total").Sums[0].Delta)
}

func TestHTTPCollector_NotReporting(t *testing.T) {
	tg := &target{}
	c := newCollector(t, tg, nil)
	assert.Equal(t, ReasonConnectionRefused, c.Scrape(context.Background()).Failure)
	assert.Equal(t, ReasonConnectionRefused, c.Scrape(context.Background()).NotReporting, "at the threshold")
	tg.set("# TYPE depth gauge\ndepth 1\n")
	assert.Empty(t, c.Scrape(context.Background()).NotReporting, "a success resets the count")
}

func TestHTTPCollector_OnceSeen(t *testing.T) {
	tg := &target{}
	c := newCollector(t, tg, func(cfg *HTTPConfig) { cfg.OnceSeen = true })
	for i := 0; i < 3; i++ {
		assert.Empty(t, c.Scrape(context.Background()).NotReporting, "never seen: not counted")
	}
	tg.set("# TYPE depth gauge\ndepth 1\n")
	c.Scrape(context.Background())
	tg.set("")
	c.Scrape(context.Background())
	assert.Equal(t, ReasonConnectionRefused, c.Scrape(context.Background()).NotReporting)
}

func TestHTTPCollector_IdentityMismatch(t *testing.T) {
	tg := &target{body: "# TYPE depth gauge\ndepth 1\n"}
	c := newCollector(t, tg, func(cfg *HTTPConfig) { cfg.Identity = "awscni_build_info" })
	assert.Equal(t, ReasonIdentityMismatch, c.Scrape(context.Background()).Failure)
	tg.set("# TYPE awscni_build_info gauge\nawscni_build_info 1\n")
	assert.Empty(t, c.Scrape(context.Background()).Failure)
}

func TestHTTPCollector_Timeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	u, _ := url.Parse(srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	c := NewHTTPCollector(HTTPConfig{Scope: "s", Endpoint: config.TargetEndpoint{Address: host, Port: p}, Timeout: 50 * time.Millisecond, MaxBytes: 1 << 20})
	require.Equal(t, ReasonTimeout, c.Scrape(context.Background()).Failure)
}

func TestHTTPCollector_LogsErrorStatusOnce(t *testing.T) {
	var logs []string
	tg := &target{}
	c := newCollector(t, tg, func(cfg *HTTPConfig) {
		cfg.Logger = funcr.New(func(_, args string) { logs = append(logs, args) }, funcr.Options{})
	})
	scrape := func() string { return c.Scrape(context.Background()).Failure }

	assert.Equal(t, ReasonConnectionRefused, scrape(), "a 503 is reported with the closed-set reason")
	scrape()
	require.Len(t, logs, 1, "the same error status is logged once")
	assert.Contains(t, logs[0], `"status"=503`)

	tg.set("depth 1\n")
	assert.Empty(t, scrape())
	tg.set("")
	scrape()
	assert.Len(t, logs, 2, "logged again after a successful scrape")
}
