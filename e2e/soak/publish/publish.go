// Package publish sends soak samples, churn stats and analysis results to
// CloudWatch, where the soak dashboard and alarms read them.
package publish

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/analysis"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/churn"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

// Namespace is the CloudWatch namespace for all soak metrics.
const Namespace = "EKSNodeMonitoringAgent/Soak"

const (
	DimensionStage = "Stage"
	DimensionPool  = "Pool"
	DimensionNode  = "Node"

	// churn metrics, all per publish interval except ChurnInFlight.
	ChurnPodsCreated       = "ChurnPodsCreated"
	ChurnPodsDeleted       = "ChurnPodsDeleted"
	ChurnCreateErrors      = "ChurnCreateErrors"
	ChurnDeleteErrors      = "ChurnDeleteErrors"
	ChurnSkippedAtCapacity = "ChurnSkippedAtCapacity"
	ChurnPodsFailed        = "ChurnPodsFailed"
	ChurnPodsNeverReady    = "ChurnPodsNeverReady"
	ChurnInFlight          = "ChurnInFlight"
	ChurnPodStartLatency   = "ChurnPodStartLatency"

	// analysis metrics, 1 when the latest window failed the check.
	LeakDetected    = "LeakDetected"
	ThresholdBreach = "ThresholdBreach"
	AgentRestarted  = "AgentRestarted"
	VerdictFailed   = "VerdictFailed"

	// Heartbeat is emitted every minute while the soak controller is alive.
	// Its alarm treats missing data as breaching.
	Heartbeat = "Heartbeat"
)

// maxDatumsPerCall stays under the PutMetricData limit of 1000 metrics.
const maxDatumsPerCall = 500

// PutMetricDataAPI is the CloudWatch API the publisher uses.
type PutMetricDataAPI interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
}

// Publisher writes soak metrics under Namespace with a Stage dimension.
type Publisher struct {
	api       PutMetricDataAPI
	stage     string
	lastChurn churn.Stats
}

// New returns a Publisher for the given pipeline stage (e.g. "beta").
func New(api PutMetricDataAPI, stage string) *Publisher {
	return &Publisher{api: api, stage: stage}
}

// PublishSamples writes every signal per node, plus the maximum across each
// pool's nodes so pool-wide alarms don't depend on node names.
func (p *Publisher) PublishSamples(ctx context.Context, samples []collector.Sample) error {
	var data []cwtypes.MetricDatum
	type poolKey struct{ pool, signal string }
	poolMax := map[poolKey]float64{}
	var latest time.Time
	for _, s := range samples {
		pool := poolName(s.Pool)
		for signal, value := range s.Values {
			data = append(data, p.datum(signal, value, s.Time,
				dim(DimensionPool, pool), dim(DimensionNode, s.Node)))
			key := poolKey{pool, signal}
			if cur, ok := poolMax[key]; !ok || value > cur {
				poolMax[key] = value
			}
		}
		if s.Time.After(latest) {
			latest = s.Time
		}
	}
	for key, value := range poolMax {
		data = append(data, p.datum(key.signal, value, latest, dim(DimensionPool, key.pool)))
	}
	return p.put(ctx, data)
}

// PublishChurn writes the churn activity since the previous call.
func (p *Publisher) PublishChurn(ctx context.Context, stats churn.Stats, now time.Time) error {
	prev := p.lastChurn
	pool := dim(DimensionPool, workload.ChurnPool)
	data := []cwtypes.MetricDatum{
		p.datum(ChurnPodsCreated, float64(stats.Created-prev.Created), now, pool),
		p.datum(ChurnPodsDeleted, float64(stats.Deleted-prev.Deleted), now, pool),
		p.datum(ChurnCreateErrors, float64(stats.CreateErrors-prev.CreateErrors), now, pool),
		p.datum(ChurnDeleteErrors, float64(stats.DeleteErrors-prev.DeleteErrors), now, pool),
		p.datum(ChurnSkippedAtCapacity, float64(stats.SkippedAtCapacity-prev.SkippedAtCapacity), now, pool),
		p.datum(ChurnPodsFailed, float64(stats.Failed-prev.Failed), now, pool),
		p.datum(ChurnPodsNeverReady, float64(stats.NeverReady-prev.NeverReady), now, pool),
		p.datum(ChurnInFlight, float64(stats.InFlight), now, pool),
	}
	if len(stats.StartLatencies) > 0 {
		set := &cwtypes.StatisticSet{
			Minimum:     aws.Float64(stats.StartLatencies[0].Seconds()),
			Maximum:     aws.Float64(stats.StartLatencies[0].Seconds()),
			Sum:         aws.Float64(0),
			SampleCount: aws.Float64(float64(len(stats.StartLatencies))),
		}
		for _, l := range stats.StartLatencies {
			v := l.Seconds()
			*set.Sum += v
			*set.Minimum = min(*set.Minimum, v)
			*set.Maximum = max(*set.Maximum, v)
		}
		d := p.datum(ChurnPodStartLatency, 0, now, pool)
		d.Value = nil
		d.StatisticValues = set
		d.Unit = cwtypes.StandardUnitSeconds
		data = append(data, d)
	}
	if err := p.put(ctx, data); err != nil {
		return err
	}
	p.lastChurn = stats
	p.lastChurn.StartLatencies = nil
	return nil
}

// PublishResult writes the outcome of an analysis window. Values are 0 or 1
// so alarms can use Maximum > 0.
func (p *Publisher) PublishResult(ctx context.Context, result analysis.Result, now time.Time) error {
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	data := []cwtypes.MetricDatum{
		p.datum(LeakDetected, flag(result.Has(analysis.CheckSlope, analysis.CheckMedianGrowth, analysis.CheckCountGrowth)), now),
		p.datum(ThresholdBreach, flag(result.Has(analysis.CheckPeak)), now),
		p.datum(AgentRestarted, flag(result.Has(analysis.CheckRestarts, analysis.CheckOOMKilled, analysis.CheckPodReplaced)), now),
		p.datum(VerdictFailed, flag(result.Verdict == analysis.VerdictFail), now),
	}
	return p.put(ctx, data)
}

// PublishHeartbeat records that the soak controller is alive.
func (p *Publisher) PublishHeartbeat(ctx context.Context, now time.Time) error {
	return p.put(ctx, []cwtypes.MetricDatum{p.datum(Heartbeat, 1, now)})
}

func (p *Publisher) datum(name string, value float64, ts time.Time, dims ...cwtypes.Dimension) cwtypes.MetricDatum {
	return cwtypes.MetricDatum{
		MetricName: aws.String(name),
		Value:      aws.Float64(value),
		Timestamp:  aws.Time(ts),
		Unit:       unitFor(name),
		Dimensions: append([]cwtypes.Dimension{dim(DimensionStage, p.stage)}, dims...),
	}
}

func (p *Publisher) put(ctx context.Context, data []cwtypes.MetricDatum) error {
	for start := 0; start < len(data); start += maxDatumsPerCall {
		end := min(start+maxDatumsPerCall, len(data))
		if _, err := p.api.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  aws.String(Namespace),
			MetricData: data[start:end],
		}); err != nil {
			return fmt.Errorf("putting %d soak metrics: %w", end-start, err)
		}
	}
	return nil
}

func unitFor(name string) cwtypes.StandardUnit {
	if strings.HasSuffix(name, "Bytes") {
		return cwtypes.StandardUnitBytes
	}
	return cwtypes.StandardUnitCount
}

func dim(name, value string) cwtypes.Dimension {
	return cwtypes.Dimension{Name: aws.String(name), Value: aws.String(value)}
}

// poolName gives nodes outside the soak pools (e.g. the system nodes) a
// stable dimension value.
func poolName(pool string) string {
	if pool == "" {
		return "other"
	}
	return pool
}
