package nodetelemetry_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

var sampleNode = nodetelemetry.NodeAttrs{
	NodeName:       "ip-10-0-0-1.ec2.internal",
	HostID:         "i-0123456789abcdef0",
	HostArch:       "amd64",
	HostType:       "c6g.xlarge",
	ZoneID:         "use1-az5",
	KubeletVersion: "v1.31.2-eks-7f9249a",
	ComputeType:    "eks-auto",
}

func decode(t *testing.T, body []byte) pmetric.Metrics {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	req := pmetricotlp.NewExportRequest()
	require.NoError(t, req.UnmarshalProto(raw))
	return req.Metrics()
}

func marshal(t *testing.T, r *nodetelemetry.Report) pmetric.Metrics {
	t.Helper()
	body, err := r.Marshal()
	require.NoError(t, err)
	return decode(t, body)
}

func metricNamed(t *testing.T, md pmetric.Metrics, scope, name string) pmetric.Metric {
	t.Helper()
	sms := md.ResourceMetrics().At(0).ScopeMetrics()
	for i := 0; i < sms.Len(); i++ {
		if sms.At(i).Scope().Name() != scope {
			continue
		}
		ms := sms.At(i).Metrics()
		for j := 0; j < ms.Len(); j++ {
			if ms.At(j).Name() == name {
				return ms.At(j)
			}
		}
	}
	t.Fatalf("%s/%s not in report", scope, name)
	return pmetric.Metric{}
}

func TestReport_ResourceAttributes(t *testing.T) {
	md := marshal(t, nodetelemetry.NewReport(nodetelemetry.NodeAttrs{NodeName: "n", HostID: "i-1"}, time.Unix(0, 0), time.Unix(300, 0)))
	attrs := md.ResourceMetrics().At(0).Resource().Attributes().AsRaw()
	assert.Equal(t, map[string]any{nodetelemetry.AttrNodeName: "n", nodetelemetry.AttrHostID: "i-1"}, attrs, "empty fields are omitted")
}

func TestReport_GaugesAndSums(t *testing.T) {
	r := nodetelemetry.NewReport(sampleNode, time.Unix(1000, 0), time.Unix(1300, 0))
	r.AddScope(nodetelemetry.Scope{Name: "ipamd", Metrics: []nodetelemetry.Metric{
		{Name: "assigned", Unit: "1", Gauges: []nodetelemetry.GaugePoint{{Value: 4}}},
		{Name: "errors", Unit: "1", Sums: []nodetelemetry.SumPoint{
			{Delta: 2, Attrs: nodetelemetry.Attrs{"api": "Describe"}},
			{Delta: 0, Attrs: nodetelemetry.Attrs{"api": "Unchanged"}},
			{Delta: 3, Attrs: nodetelemetry.Attrs{"api": "Carried"}, StartTime: time.Unix(400, 0)},
		}},
	}})
	md := marshal(t, r)

	g := metricNamed(t, md, "ipamd", "assigned").Gauge().DataPoints()
	require.Equal(t, 1, g.Len())
	assert.Equal(t, 4.0, g.At(0).DoubleValue())
	assert.Equal(t, time.Unix(1300, 0).UTC(), g.At(0).Timestamp().AsTime())

	sum := metricNamed(t, md, "ipamd", "errors").Sum()
	assert.Equal(t, pmetric.AggregationTemporalityDelta, sum.AggregationTemporality())
	assert.True(t, sum.IsMonotonic())
	require.Equal(t, 2, sum.DataPoints().Len(), "zero deltas are not sent")
	assert.Equal(t, time.Unix(1000, 0).UTC(), sum.DataPoints().At(0).StartTimestamp().AsTime())
	assert.Equal(t, time.Unix(400, 0).UTC(), sum.DataPoints().At(1).StartTimestamp().AsTime(), "a carried delta keeps its baseline time")
}

func TestReport_DropsInvalidAndExtraPoints(t *testing.T) {
	r := nodetelemetry.NewReport(sampleNode, time.Unix(0, 0), time.Unix(300, 0))
	pts := []nodetelemetry.GaugePoint{{Value: 1, Attrs: nodetelemetry.Attrs{"pod": "has space"}}}
	for i := 0; i < nodetelemetry.MaxPointsPerMetric+5; i++ {
		pts = append(pts, nodetelemetry.GaugePoint{Value: 1, Attrs: nodetelemetry.Attrs{"api": fmt.Sprintf("a%d", i)}})
	}
	r.AddScope(nodetelemetry.Scope{Name: "s", Metrics: []nodetelemetry.Metric{{Name: "g", Unit: "1", Gauges: pts}}})
	assert.Equal(t, nodetelemetry.MaxPointsPerMetric, metricNamed(t, marshal(t, r), "s", "g").Gauge().DataPoints().Len())
}

func TestReport_ProblemsAndNotReporting(t *testing.T) {
	r := nodetelemetry.NewReport(sampleNode, time.Unix(0, 0), time.Unix(300, 0))
	r.AddScope(nodetelemetry.Scope{Name: "ipamd", Problems: []nodetelemetry.Problem{
		{Type: "PodIPAllocationFailed", Reason: "InsufficientCidrBlocks", Resources: map[string]string{"subnetId": "subnet-0abc"}},
		{Type: "IAMPermissionMissing", Reason: "UnauthorizedOperation"},
		{Type: "Bad", Reason: "free text"},
	}})
	r.AddScope(nodetelemetry.Scope{
		Name:         "kube-proxy",
		Metrics:      []nodetelemetry.Metric{{Name: "g", Unit: "1", Gauges: []nodetelemetry.GaugePoint{{Value: 1}}}},
		NotReporting: "Timeout",
	})
	md := marshal(t, r)

	problems := metricNamed(t, md, "ipamd", nodetelemetry.MetricEksProblem).Gauge().DataPoints()
	require.Equal(t, 2, problems.Len(), "invalid values are dropped")
	assert.Equal(t, map[string]any{
		"problem.type": "PodIPAllocationFailed", "problem.reason": "InsufficientCidrBlocks", "resource.subnetId": "subnet-0abc",
	}, problems.At(0).Attributes().AsRaw())
	assert.Equal(t, int64(1), problems.At(0).IntValue())

	nr := metricNamed(t, md, "kube-proxy", nodetelemetry.MetricEksNotReporting).Gauge().DataPoints()
	require.Equal(t, 1, nr.Len())
	assert.Equal(t, map[string]any{"reason": "Timeout"}, nr.At(0).Attributes().AsRaw())
	assert.Equal(t, 1, md.ResourceMetrics().At(0).ScopeMetrics().At(1).Metrics().Len(), "a not-reporting scope carries only eks_not_reporting")
}

func TestReport_SkipsMetricsWithoutAKnownType(t *testing.T) {
	r := nodetelemetry.NewReport(sampleNode, time.Unix(0, 0), time.Unix(300, 0))
	r.AddScope(nodetelemetry.Scope{Name: "s", Metrics: []nodetelemetry.Metric{
		{Name: "typed", Unit: "1", Gauges: []nodetelemetry.GaugePoint{{Value: 1}}},
		{Name: "untyped", Unit: "1"},
		{Name: "both", Unit: "1", Gauges: []nodetelemetry.GaugePoint{{Value: 1}}, Sums: []nodetelemetry.SumPoint{{Delta: 1}}},
	}})
	ms := marshal(t, r).ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	require.Equal(t, 1, ms.Len(), "a metric needs exactly one of gauges or sums")
	assert.Equal(t, "typed", ms.At(0).Name())
}
