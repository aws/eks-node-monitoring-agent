package telemetry

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"sigs.k8s.io/yaml"

	nmaconfig "github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

type agent struct {
	mu    sync.Mutex
	value int
	down  bool
}

func (a *agent) set(v int, down bool) { a.mu.Lock(); a.value, a.down = v, down; a.mu.Unlock() }

func (a *agent) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintf(w, "# TYPE awscni_build_info gauge\nawscni_build_info 1\n# TYPE awscni_aws_api_error_count counter\nawscni_aws_api_error_count{api=\"Describe\",error=\"x\"} %d\n", a.value)
}

type extension struct {
	mu      sync.Mutex
	status  []int
	reports []pmetric.Metrics
}

func (e *extension) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	zr, err := gzip.NewReader(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	raw, _ := io.ReadAll(zr)
	req := pmetricotlp.NewExportRequest()
	if err := req.UnmarshalProto(raw); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reports = append(e.reports, req.Metrics())
	status := http.StatusOK
	if len(e.status) > 0 {
		status, e.status = e.status[0], e.status[1:]
	}
	w.WriteHeader(status)
}

func scopeMetrics(md pmetric.Metrics, scope string) map[string]pmetric.Metric {
	out := map[string]pmetric.Metric{}
	sms := md.ResourceMetrics().At(0).ScopeMetrics()
	for i := 0; i < sms.Len(); i++ {
		if sms.At(i).Scope().Name() == scope {
			for j := 0; j < sms.At(i).Metrics().Len(); j++ {
				m := sms.At(i).Metrics().At(j)
				out[m.Name()] = m
			}
		}
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	ipamd := &agent{}
	agentSrv := httptest.NewServer(ipamd)
	t.Cleanup(agentSrv.Close)
	u, _ := url.Parse(agentSrv.URL)
	host, port, _ := net.SplitHostPort(u.Host)

	ext := &extension{status: []int{200, 200, 503, 200, 400, 200, 200, 200, 200}}
	extSrv := httptest.NewServer(ext)
	t.Cleanup(extSrv.Close)

	cfg := &config.Config{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(fmt.Sprintf(`
enabled: true
targets:
  - component: ipamd
    endpoint: { address: %s, port: %s }
    identity: { requireMetric: awscni_build_info }
    metrics:
      - { name: awscni_aws_api_error_count, type: counter, unit: "1", labels: [api] }
  - component: node-monitoring-agent
    metrics:
      - { name: eks_problem }
`, host, port)), cfg))
	fatal := fatals{{ConditionType: "StorageReady", Reason: "IOError"}}
	collectors, wait := BuildCollectors(cfg, fatal, map[string]string{"instanceId": "i-0123456789abcdef0"})
	require.Len(t, collectors, 2)

	runtime := &nmaconfig.RuntimeContext{}
	m := NewManager(ManagerConfig{
		Runtime:    runtime,
		Node:       nodetelemetry.NodeAttrs{NodeName: "n"},
		Collectors: collectors,
		ScrapeWait: wait,
		Sender:     nodetelemetry.NewClientFromURL(extSrv.Client(), extSrv.URL),
		Logger:     logr.Discard(),
	})

	type want struct {
		value        int
		down         bool
		delta        float64
		notReporting string
	}
	steps := []want{
		{value: 10},
		{value: 12, delta: 2},
		{value: 15, delta: 3},
		{value: 16, delta: 4},
		{value: 20, delta: 4},
		{value: 21, delta: 1},
		{down: true},
		{down: true, notReporting: "ConnectionRefused"},
		{value: 30, delta: 9},
	}
	for i, s := range steps {
		if i == 3 {
			runtime.AddTags(nmaconfig.Hybrid)
		}
		ipamd.set(s.value, s.down)
		m.runInterval(context.Background(), time.Unix(int64(i)*300, 0), time.Unix(int64(i+1)*300, 0))
		ext.mu.Lock()
		require.Len(t, ext.reports, i+1, "one report per interval")
		md := ext.reports[i]
		ext.mu.Unlock()

		computeType, ok := md.ResourceMetrics().At(0).Resource().Attributes().Get(nodetelemetry.AttrComputeType)
		if i < 3 {
			assert.False(t, ok, "step %d: no compute type without a tag", i)
		} else {
			assert.Equal(t, nmaconfig.Hybrid, computeType.Str(), "step %d: a tag added after start is picked up", i)
		}

		got := scopeMetrics(md, "ipamd")
		if s.delta == 0 {
			assert.NotContains(t, got, "awscni_aws_api_error_count", "step %d", i)
		} else if assert.Contains(t, got, "awscni_aws_api_error_count", "step %d", i) {
			dp := got["awscni_aws_api_error_count"].Sum().DataPoints().At(0)
			assert.Equal(t, s.delta, dp.DoubleValue(), "step %d", i)
			assert.Equal(t, map[string]any{"api": "Describe"}, dp.Attributes().AsRaw(), "step %d: only kept labels", i)
		}
		if s.notReporting != "" {
			assert.Contains(t, got, nodetelemetry.MetricEksNotReporting, "step %d", i)
		} else {
			assert.NotContains(t, got, nodetelemetry.MetricEksNotReporting, "step %d", i)
		}

		problems := scopeMetrics(md, NMAScope)[nodetelemetry.MetricEksProblem].Gauge().DataPoints()
		require.Equal(t, 1, problems.Len(), "step %d", i)
		assert.Equal(t, map[string]any{"problem.type": "StorageReady", "problem.reason": "IOError", "resource.instanceId": "i-0123456789abcdef0"}, problems.At(0).Attributes().AsRaw())
	}
}
