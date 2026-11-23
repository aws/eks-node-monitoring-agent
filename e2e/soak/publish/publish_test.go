package publish

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/analysis"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/churn"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
)

type fakeCloudWatch struct {
	calls []*cloudwatch.PutMetricDataInput
	err   error
}

func (f *fakeCloudWatch) PutMetricData(_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error) {
	f.calls = append(f.calls, in)
	return &cloudwatch.PutMetricDataOutput{}, f.err
}

func (f *fakeCloudWatch) data() []cwtypes.MetricDatum {
	var out []cwtypes.MetricDatum
	for _, c := range f.calls {
		out = append(out, c.MetricData...)
	}
	return out
}

func dims(d cwtypes.MetricDatum) map[string]string {
	out := map[string]string{}
	for _, dim := range d.Dimensions {
		out[aws.ToString(dim.Name)] = aws.ToString(dim.Value)
	}
	return out
}

// find returns the datum with the given name and exact dimensions.
func find(t *testing.T, data []cwtypes.MetricDatum, name string, want map[string]string) cwtypes.MetricDatum {
	t.Helper()
	for _, d := range data {
		if aws.ToString(d.MetricName) == name && assert.ObjectsAreEqual(want, dims(d)) {
			return d
		}
	}
	t.Fatalf("no %s datum with dimensions %v", name, want)
	return cwtypes.MetricDatum{}
}

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func TestPublishSamples(t *testing.T) {
	cw := &fakeCloudWatch{}
	samples := []collector.Sample{
		{Time: now, Node: "node-a", Pool: "soak", Values: map[string]float64{collector.HeapInuse: 10, collector.Goroutines: 90}},
		{Time: now, Node: "node-b", Pool: "soak", Values: map[string]float64{collector.HeapInuse: 30, collector.Goroutines: 80}},
		{Time: now, Node: "sys-a", Values: map[string]float64{collector.HeapInuse: 50}},
	}
	require.NoError(t, New(cw, "beta").PublishSamples(context.Background(), samples))

	require.Len(t, cw.calls, 1)
	assert.Equal(t, Namespace, aws.ToString(cw.calls[0].Namespace))
	data := cw.data()
	assert.Len(t, data, 5+3, "one datum per node signal plus one max per pool signal")

	perNode := find(t, data, collector.HeapInuse, map[string]string{"Stage": "beta", "Pool": "soak", "Node": "node-a"})
	assert.Equal(t, 10.0, aws.ToFloat64(perNode.Value))
	assert.Equal(t, cwtypes.StandardUnitBytes, perNode.Unit)
	assert.Equal(t, now, aws.ToTime(perNode.Timestamp))

	poolHeap := find(t, data, collector.HeapInuse, map[string]string{"Stage": "beta", "Pool": "soak"})
	assert.Equal(t, 30.0, aws.ToFloat64(poolHeap.Value), "the pool aggregate is the worst node")
	poolGoroutines := find(t, data, collector.Goroutines, map[string]string{"Stage": "beta", "Pool": "soak"})
	assert.Equal(t, 90.0, aws.ToFloat64(poolGoroutines.Value))
	assert.Equal(t, cwtypes.StandardUnitCount, poolGoroutines.Unit)

	find(t, data, collector.HeapInuse, map[string]string{"Stage": "beta", "Pool": "other", "Node": "sys-a"})
}

func TestPublishSamplesBatches(t *testing.T) {
	cw := &fakeCloudWatch{}
	var samples []collector.Sample
	for i := range 700 {
		samples = append(samples, collector.Sample{Time: now, Node: fmt.Sprintf("n%d", i), Pool: "soak",
			Values: map[string]float64{collector.HeapInuse: float64(i)}})
	}
	require.NoError(t, New(cw, "beta").PublishSamples(context.Background(), samples))
	require.Len(t, cw.calls, 2)
	for _, c := range cw.calls {
		assert.LessOrEqual(t, len(c.MetricData), maxDatumsPerCall)
	}
	assert.Len(t, cw.data(), 701)
}

func TestPublishChurnSendsDeltas(t *testing.T) {
	cw := &fakeCloudWatch{}
	p := New(cw, "beta")
	ctx := context.Background()
	churnDims := map[string]string{"Stage": "beta", "Pool": "churn"}

	require.NoError(t, p.PublishChurn(ctx, churn.Stats{
		Created: 120, Deleted: 100, NeverReady: 2, InFlight: 118,
		StartLatencies: []time.Duration{2 * time.Second, 4 * time.Second, 3 * time.Second},
	}, now))
	data := cw.data()
	assert.Equal(t, 120.0, aws.ToFloat64(find(t, data, ChurnPodsCreated, churnDims).Value))
	assert.Equal(t, 118.0, aws.ToFloat64(find(t, data, ChurnInFlight, churnDims).Value))
	latency := find(t, data, ChurnPodStartLatency, churnDims)
	assert.Nil(t, latency.Value)
	require.NotNil(t, latency.StatisticValues)
	assert.Equal(t, 2.0, aws.ToFloat64(latency.StatisticValues.Minimum))
	assert.Equal(t, 4.0, aws.ToFloat64(latency.StatisticValues.Maximum))
	assert.Equal(t, 9.0, aws.ToFloat64(latency.StatisticValues.Sum))
	assert.Equal(t, 3.0, aws.ToFloat64(latency.StatisticValues.SampleCount))
	assert.Equal(t, cwtypes.StandardUnitSeconds, latency.Unit)

	cw.calls = nil
	require.NoError(t, p.PublishChurn(ctx, churn.Stats{Created: 240, Deleted: 220, NeverReady: 2, InFlight: 120}, now.Add(time.Minute)))
	data = cw.data()
	assert.Equal(t, 120.0, aws.ToFloat64(find(t, data, ChurnPodsCreated, churnDims).Value), "counters are published as per-interval deltas")
	assert.Equal(t, 0.0, aws.ToFloat64(find(t, data, ChurnPodsNeverReady, churnDims).Value))
	for _, d := range data {
		assert.NotEqual(t, ChurnPodStartLatency, aws.ToString(d.MetricName), "no latency datum without samples")
	}
}

func TestPublishChurnRetriesDeltaAfterFailure(t *testing.T) {
	cw := &fakeCloudWatch{err: errors.New("throttled")}
	p := New(cw, "beta")
	require.Error(t, p.PublishChurn(context.Background(), churn.Stats{Created: 120}, now))

	cw.err, cw.calls = nil, nil
	require.NoError(t, p.PublishChurn(context.Background(), churn.Stats{Created: 240}, now))
	created := find(t, cw.data(), ChurnPodsCreated, map[string]string{"Stage": "beta", "Pool": "churn"})
	assert.Equal(t, 240.0, aws.ToFloat64(created.Value), "a failed publish must not drop its interval")
}

func TestPublishResult(t *testing.T) {
	stageDims := map[string]string{"Stage": "beta"}
	for name, tc := range map[string]struct {
		result analysis.Result
		want   map[string]float64
	}{
		"pass": {
			result: analysis.Result{Verdict: analysis.VerdictPass},
			want:   map[string]float64{LeakDetected: 0, ThresholdBreach: 0, AgentRestarted: 0, VerdictFailed: 0},
		},
		"leak": {
			result: analysis.Result{Verdict: analysis.VerdictFail, Findings: []analysis.Finding{{Check: analysis.CheckSlope}}},
			want:   map[string]float64{LeakDetected: 1, ThresholdBreach: 0, AgentRestarted: 0, VerdictFailed: 1},
		},
		"record only": {
			result: analysis.Result{Verdict: analysis.VerdictRecordOnly, Findings: []analysis.Finding{{Check: analysis.CheckPeak}, {Check: analysis.CheckOOMKilled}}},
			want:   map[string]float64{LeakDetected: 0, ThresholdBreach: 1, AgentRestarted: 1, VerdictFailed: 0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cw := &fakeCloudWatch{}
			require.NoError(t, New(cw, "beta").PublishResult(context.Background(), tc.result, now))
			for metric, want := range tc.want {
				assert.Equal(t, want, aws.ToFloat64(find(t, cw.data(), metric, stageDims).Value), metric)
			}
		})
	}
}

func TestPublishHeartbeat(t *testing.T) {
	cw := &fakeCloudWatch{}
	require.NoError(t, New(cw, "beta").PublishHeartbeat(context.Background(), now))
	hb := find(t, cw.data(), Heartbeat, map[string]string{"Stage": "beta"})
	assert.Equal(t, 1.0, aws.ToFloat64(hb.Value))
}
