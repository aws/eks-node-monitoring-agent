package scrape

import (
	"bytes"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

const textCT = "text/plain; version=0.0.4"

var testAllow = map[string]config.MetricSpec{
	"foo_total":                    {Name: "foo_total", Type: config.MetricTypeCounter, MaxSeries: 2},
	nodetelemetry.MetricEksProblem: {Name: nodetelemetry.MetricEksProblem},
}

func TestParse_Text(t *testing.T) {
	body := `
# TYPE foo_total counter
foo_total{api="A"} 10
foo_total{api="B"} 20
# TYPE not_allowlisted gauge
not_allowlisted 1
# TYPE build_info gauge
build_info{version="1"} 1
# TYPE eks_problem gauge
eks_problem{type="PodIPAllocationFailed",reason="InsufficientCidrBlocks",resource_subnet_id="subnet-0abc"} 1
eks_problem{type="Cleared",reason="Cleared"} 0
`
	res, err := Parse(textCT, strings.NewReader(body), testAllow, 1<<20, "build_info")
	require.NoError(t, err)
	assert.Equal(t, []Series{{Labels: map[string]string{"api": "A"}, Value: 10}, {Labels: map[string]string{"api": "B"}, Value: 20}}, res.Series["foo_total"])
	assert.NotContains(t, res.Series, "not_allowlisted")
	assert.True(t, res.SawIdentity)
	assert.Equal(t, []nodetelemetry.Problem{{Type: "PodIPAllocationFailed", Reason: "InsufficientCidrBlocks", Resources: map[string]string{"subnetId": "subnet-0abc"}}}, res.Problems, "only active problems")
}

func TestParse_Protobuf(t *testing.T) {
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeProtoDelim))
	name, v := "foo_total", 5.0
	require.NoError(t, enc.Encode(&dto.MetricFamily{
		Name:   &name,
		Type:   dto.MetricType_COUNTER.Enum(),
		Metric: []*dto.Metric{{Counter: &dto.Counter{Value: &v}}},
	}))
	res, err := Parse(string(expfmt.NewFormat(expfmt.TypeProtoDelim)), &buf, testAllow, 1<<20, "")
	require.NoError(t, err)
	assert.Equal(t, []Series{{Labels: map[string]string{}, Value: 5}}, res.Series["foo_total"])
}

func TestParse_MaxSeriesCountsSentSeries(t *testing.T) {
	body := "# TYPE foo_total counter\n" +
		"foo_total{api=\"A\",code=\"200\"} 1\nfoo_total{api=\"A\",code=\"500\"} 1\n" +
		"foo_total{api=\"B\",code=\"200\"} 1\nfoo_total{api=\"B\",code=\"500\"} 1\n"
	byAPI := map[string]config.MetricSpec{"foo_total": {Name: "foo_total", Type: config.MetricTypeCounter, Labels: []string{"api"}, MaxSeries: 2}}
	res, err := Parse(textCT, strings.NewReader(body), byAPI, 1<<20, "")
	require.NoError(t, err)
	assert.Len(t, res.Series["foo_total"], 4, "4 source series, but only 2 distinct api values are sent, which is within MaxSeries")

	byCode := map[string]config.MetricSpec{"foo_total": {Name: "foo_total", Type: config.MetricTypeCounter, Labels: []string{"api", "code"}, MaxSeries: 2}}
	res, err = Parse(textCT, strings.NewReader(body), byCode, 1<<20, "")
	require.NoError(t, err)
	assert.NotContains(t, res.Series, "foo_total", "4 distinct api/code pairs would be sent, which exceeds MaxSeries")
}

func TestParse_TooLarge(t *testing.T) {
	body := "# TYPE foo_total counter\nfoo_total 1\n"
	_, err := Parse(textCT, strings.NewReader(body), testAllow, 10, "")
	assert.ErrorIs(t, err, errTooLarge)
	_, err = Parse(textCT, strings.NewReader(body), testAllow, int64(len(body)), "")
	assert.NoError(t, err, "exactly maxBytes is fine")
}
