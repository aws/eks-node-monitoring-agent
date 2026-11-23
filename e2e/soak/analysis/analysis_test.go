package analysis

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
)

const mi = 1 << 20

var windowStart = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

// enforcing returns the checked-in thresholds with enforcement on, so tests
// see FAIL rather than RECORD_ONLY.
func enforcing(t *testing.T) Thresholds {
	t.Helper()
	th, err := DefaultThresholds()
	require.NoError(t, err)
	th.RecordOnly = false
	return th
}

// healthy is a flat agent: values well under the placeholder peaks, with a
// GC-like sawtooth on memory and a little jitter on counts.
func healthy(h float64, i int) map[string]float64 {
	saw := float64(i%20) / 20 // 0..1 over 10 minutes
	return map[string]float64{
		collector.HeapInuse:    30*mi + 4*mi*saw,
		collector.HeapAlloc:    24*mi + 4*mi*saw,
		collector.RuntimeSys:   60 * mi,
		collector.RSS:          80*mi + 2*mi*saw,
		collector.WorkingSet:   82*mi + 2*mi*saw,
		collector.ContainerRSS: 24*mi + 2*mi*saw,
		collector.Goroutines:   100 + float64(i%5),
		collector.OpenFDs:      30 + float64(i%3),
		collector.Restarts:     0,
		collector.OOMKilled:    0,
	}
}

// series builds one node's samples every 30s over duration. values gets the
// hours since the window start and the sample index.
func series(node string, duration time.Duration, values func(h float64, i int) map[string]float64) []collector.Sample {
	var samples []collector.Sample
	for i := 0; time.Duration(i)*30*time.Second <= duration; i++ {
		ts := windowStart.Add(time.Duration(i) * 30 * time.Second)
		samples = append(samples, collector.Sample{
			Time: ts, Node: node, Pod: "nma-" + node, Pool: "soak",
			Values: values(ts.Sub(windowStart).Hours(), i),
		})
	}
	return samples
}

func with(base func(float64, int) map[string]float64, mutate func(h float64, i int, v map[string]float64)) func(float64, int) map[string]float64 {
	return func(h float64, i int) map[string]float64 {
		v := base(h, i)
		mutate(h, i, v)
		return v
	}
}

func analyze(t *testing.T, samples []collector.Sample, duration time.Duration, th Thresholds) Result {
	t.Helper()
	return Analyze(samples, windowStart, windowStart.Add(duration), th)
}

func checks(r Result) []Check {
	var out []Check
	for _, f := range r.Findings {
		out = append(out, f.Check)
	}
	return out
}

// sixHours is one node's samples over a 6h window.
func sixHours(values func(float64, int) map[string]float64) []collector.Sample {
	return series("node-a", 6*time.Hour, values)
}

// replacePod gives the second half of the samples a new agent pod, and from
// three quarters on that pod has restarted once when restartNewPod is set.
func replacePod(samples []collector.Sample, restartNewPod bool) []collector.Sample {
	for i := len(samples) / 2; i < len(samples); i++ {
		samples[i].Pod = "nma-node-a-new"
		if restartNewPod && i > 3*len(samples)/4 {
			samples[i].Values[collector.Restarts] = 1
		}
	}
	return samples
}

// Patterns a healthy agent shows that must not fail the gate.
func TestHealthyPatternsPass(t *testing.T) {
	for name, samples := range map[string][]collector.Sample{
		"flat run on several nodes": append(append(sixHours(healthy),
			series("node-b", 6*time.Hour, healthy)...), series("churn-a", 6*time.Hour, healthy)...),
		// memory climbs for the first 20 minutes after start, then holds flat.
		"warm-up ramp": sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
			if ramp := min(h*3, 1); ramp < 1 {
				v[collector.HeapInuse] = 10*mi + 20*mi*ramp
				v[collector.RuntimeSys] = 30*mi + 30*mi*ramp
			}
		})),
		// what the dev soak showed on a clean build: process RSS climbing
		// ~10Mi/h past 150Mi from memory-mapped journal files, with flat
		// anonymous memory. Process RSS isn't gated.
		"file-backed RSS growth": sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
			v[collector.RSS] = 150*mi + 10*mi*h
		})),
		// the restart count and OOMKilled reason carry over from a kill before
		// the window, but nothing happens during it.
		"OOM kill before the window": sixHours(with(healthy, func(_ float64, _ int, v map[string]float64) {
			v[collector.Restarts], v[collector.OOMKilled] = 2, 1
		})),
	} {
		t.Run(name, func(t *testing.T) {
			r := analyze(t, samples, 6*time.Hour, enforcing(t))
			assert.Equal(t, VerdictPass, r.Verdict, "findings: %+v", r.Findings)
			assert.Empty(t, r.Notes)
		})
	}

	r := analyze(t, sixHours(healthy), 6*time.Hour, enforcing(t))
	assert.InDelta(t, 34*mi, r.Signals[collector.HeapInuse].MaxPeak, mi, "peaks are summarised for calibration")
}

// Each kind of leak, kept under its peak threshold, is caught by the trend
// checks on the right signal.
func TestLeaksAreCaught(t *testing.T) {
	trend := []Check{CheckSlope, CheckMedianGrowth}
	for name, tc := range map[string]struct {
		signal string
		grow   func(h float64) float64
		want   []Check
	}{
		// 4Mi/h is still under the 60Mi heap peak after 6h.
		"Go heap":           {collector.HeapInuse, func(h float64) float64 { return 4 * mi * h }, trend},
		"Go runtime memory": {collector.RuntimeSys, func(h float64) float64 { return 4 * mi * h }, trend},
		// anonymous memory outside the Go heap, e.g. a cgo leak.
		"anonymous RSS": {collector.ContainerRSS, func(h float64) float64 { return 4 * mi * h }, trend},
		// 8Mi/h stays under the 150Mi working-set peak after 6h.
		"working set": {collector.WorkingSet, func(h float64) float64 { return 8 * mi * h }, trend},
		"goroutines":  {collector.Goroutines, func(h float64) float64 { return 20 * h }, []Check{CheckCountGrowth}},
		"open FDs":    {collector.OpenFDs, func(h float64) float64 { return math.Floor(10 * h) }, []Check{CheckCountGrowth}},
	} {
		t.Run(name, func(t *testing.T) {
			r := analyze(t, sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
				v[tc.signal] += tc.grow(h)
			})), 6*time.Hour, enforcing(t))
			assert.Equal(t, VerdictFail, r.Verdict)
			assert.ElementsMatch(t, tc.want, checks(r))
			for _, f := range r.Findings {
				assert.Equal(t, "node-a", f.Node)
				assert.Equal(t, tc.signal, f.Signal)
			}
		})
	}

	r := analyze(t, sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
		v[collector.HeapInuse] += 4 * mi * h
	})), 6*time.Hour, enforcing(t))
	assert.InDelta(t, 4*mi, r.Signals[collector.HeapInuse].MaxSlopePerHour, 0.2*mi, "slopes are summarised for calibration")
}

// A single spike over a peak threshold fails the peak check only, wherever it
// lands in the window.
func TestSpikesFailPeak(t *testing.T) {
	for name, tc := range map[string]struct {
		signal string
		index  int
		value  float64
	}{
		"working set mid-window":    {collector.WorkingSet, 300, 170 * mi},
		"goroutines during warm-up": {collector.Goroutines, 5, 900},
	} {
		t.Run(name, func(t *testing.T) {
			r := analyze(t, sixHours(with(healthy, func(_ float64, i int, v map[string]float64) {
				if i == tc.index {
					v[tc.signal] = tc.value
				}
			})), 6*time.Hour, enforcing(t))
			require.Equal(t, []Check{CheckPeak}, checks(r), "a spike breaks the peak but not the trend")
			assert.Equal(t, tc.signal, r.Findings[0].Signal)
			assert.EqualValues(t, tc.value, r.Findings[0].Value)
		})
	}
}

// Restarts, OOM kills and pod replacements during the window each fail it,
// counted once per event.
func TestRestartsFail(t *testing.T) {
	for name, tc := range map[string]struct {
		samples []collector.Sample
		want    []Check
		count   float64
	}{
		"restart": {
			samples: sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
				if h > 3 {
					v[collector.Restarts] = 1
				}
			})),
			want: []Check{CheckRestarts}, count: 1,
		},
		"OOM kill": {
			samples: sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
				if h > 3 {
					v[collector.Restarts], v[collector.OOMKilled] = 1, 1
				}
			})),
			want: []Check{CheckRestarts, CheckOOMKilled}, count: 1,
		},
		// the OOMKilled reason stays set between the two kills.
		"two OOM kills": {
			samples: sixHours(with(healthy, func(h float64, _ int, v map[string]float64) {
				switch {
				case h > 4:
					v[collector.Restarts], v[collector.OOMKilled] = 2, 1
				case h > 2:
					v[collector.Restarts], v[collector.OOMKilled] = 1, 1
				}
			})),
			want: []Check{CheckRestarts, CheckOOMKilled}, count: 2,
		},
		// a new pod's reset restart count isn't a separate finding.
		"pod replaced": {
			samples: replacePod(sixHours(healthy), false),
			want:    []Check{CheckPodReplaced},
		},
		"restart after pod replacement": {
			samples: replacePod(sixHours(healthy), true),
			want:    []Check{CheckPodReplaced, CheckRestarts},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := analyze(t, tc.samples, 6*time.Hour, enforcing(t))
			require.ElementsMatch(t, tc.want, checks(r))
			if tc.count > 0 {
				for _, f := range r.Findings {
					assert.EqualValues(t, tc.count, f.Value, "%s count", f.Check)
				}
			}
		})
	}
}

func TestNoDataFails(t *testing.T) {
	r := analyze(t, nil, 6*time.Hour, enforcing(t))
	assert.Equal(t, VerdictFail, r.Verdict, "a window with no samples must never pass")
	assert.Equal(t, []Check{CheckNoData}, checks(r))

	outside := series("node-a", time.Hour, healthy)
	r = Analyze(outside, windowStart.Add(2*time.Hour), windowStart.Add(8*time.Hour), enforcing(t))
	assert.Equal(t, []Check{CheckNoData}, checks(r), "samples outside the window are ignored")
}

func TestShortWindowSkipsTrendsButChecksPeaks(t *testing.T) {
	leak := with(healthy, func(h float64, _ int, v map[string]float64) {
		v[collector.HeapInuse] += 10 * mi * h
		v[collector.OpenFDs] = 250
	})
	r := analyze(t, series("node-a", 90*time.Minute, leak), 90*time.Minute, enforcing(t))
	assert.Equal(t, []Check{CheckPeak}, checks(r))
	require.Len(t, r.Notes, 1)
	assert.Contains(t, r.Notes[0], "skipped trend checks")
}

func TestRecordOnly(t *testing.T) {
	th := enforcing(t)
	th.RecordOnly = true
	leak := with(healthy, func(h float64, _ int, v map[string]float64) {
		v[collector.HeapInuse] += 5 * mi * h
	})
	r := analyze(t, series("node-a", 6*time.Hour, leak), 6*time.Hour, th)
	assert.Equal(t, VerdictRecordOnly, r.Verdict)
	assert.NotEmpty(t, r.Findings, "record-only still reports what would have failed")
}

func TestDefaultThresholdsStartRecordOnly(t *testing.T) {
	th, err := DefaultThresholds()
	require.NoError(t, err)
	assert.True(t, th.RecordOnly, "keep recordOnly until thresholds are calibrated from real runs")
	for _, signal := range gatedSignals {
		assert.Contains(t, th.Peak, signal, "every gated signal needs a peak threshold")
	}
	assert.NotContains(t, th.Peak, collector.RSS, "process RSS is mostly file-backed and must not be gated")
	for _, signal := range []string{collector.HeapInuse, collector.RuntimeSys, collector.ContainerRSS, collector.WorkingSet} {
		assert.Contains(t, th.SlopePerHour, signal, "%s must be checked for a flat trend", signal)
		assert.Contains(t, th.MedianGrowthPercent, signal, "%s must be checked for median growth", signal)
	}
}

// gatedSignals are the sampled signals the leak gate checks. Process RSS is
// sampled but deliberately not gated (see thresholds.yaml).
var gatedSignals = []string{
	collector.HeapInuse, collector.HeapAlloc, collector.RuntimeSys, collector.ContainerRSS,
	collector.WorkingSet, collector.Goroutines, collector.OpenFDs,
}

// Memory thresholds must leave headroom under the agent's memory limit, so a
// passing release can't be OOM killed in production.
func TestMemoryPeaksAreUnderChartLimit(t *testing.T) {
	data, err := os.ReadFile("../../../charts/eks-node-monitoring-agent/values.yaml")
	require.NoError(t, err)
	var values struct {
		NodeAgent struct {
			Resources struct {
				Limits struct {
					Memory resource.Quantity `json:"memory"`
				} `json:"limits"`
			} `json:"resources"`
		} `json:"nodeAgent"`
	}
	require.NoError(t, yaml.Unmarshal(data, &values))
	limit := values.NodeAgent.Resources.Limits.Memory.AsApproximateFloat64()
	require.Positive(t, limit, "nodeAgent.resources.limits.memory not found")

	th, err := DefaultThresholds()
	require.NoError(t, err)
	for _, signal := range collector.MemorySignals {
		peak, ok := th.Peak[signal]
		if signal == collector.RSS {
			continue
		}
		require.True(t, ok, "missing peak for %s", signal)
		assert.LessOrEqual(t, peak.AsApproximateFloat64(), 0.8*limit,
			"%s peak %s is over 80%% of the chart memory limit %s", signal, peak.String(), values.NodeAgent.Resources.Limits.Memory.String())
	}
}

func TestParseThresholdsValidation(t *testing.T) {
	_, err := ParseThresholds([]byte("medianWindow: 1h\nminTrendWindow: 2h\nunknownKey: 1\n"))
	assert.Error(t, err, "typos in thresholds.yaml must not be silently ignored")

	_, err = ParseThresholds([]byte("medianWindow: 1h\nminTrendWindow: 90m\n"))
	assert.ErrorContains(t, err, "at least twice")
}
