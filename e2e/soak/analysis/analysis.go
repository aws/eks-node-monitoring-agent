// Package analysis decides whether an NMA soak window shows a leak. It checks
// each node's agent independently for peaks over threshold, heap and runtime
// memory that isn't flat, goroutine and FD growth, and restarts.
package analysis

import (
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
)

//go:embed thresholds.yaml
var defaultThresholds []byte

// Thresholds configure the leak checks. See thresholds.yaml.
type Thresholds struct {
	RecordOnly          bool                         `json:"recordOnly"`
	WarmUp              metav1.Duration              `json:"warmUp"`
	MinTrendWindow      metav1.Duration              `json:"minTrendWindow"`
	MedianWindow        metav1.Duration              `json:"medianWindow"`
	Peak                map[string]resource.Quantity `json:"peak"`
	SlopePerHour        map[string]resource.Quantity `json:"slopePerHour"`
	MedianGrowthPercent map[string]float64           `json:"medianGrowthPercent"`
	CountGrowth         map[string]resource.Quantity `json:"countGrowth"`
	MaxRestarts         int                          `json:"maxRestarts"`
}

// DefaultThresholds returns the thresholds checked in with the code.
func DefaultThresholds() (Thresholds, error) {
	return ParseThresholds(defaultThresholds)
}

// ParseThresholds parses a thresholds document.
func ParseThresholds(data []byte) (Thresholds, error) {
	var th Thresholds
	if err := yaml.UnmarshalStrict(data, &th); err != nil {
		return th, fmt.Errorf("parsing thresholds: %w", err)
	}
	if th.MedianWindow.Duration <= 0 || th.MinTrendWindow.Duration < 2*th.MedianWindow.Duration {
		return th, fmt.Errorf("minTrendWindow (%s) must be at least twice medianWindow (%s)", th.MinTrendWindow.Duration, th.MedianWindow.Duration)
	}
	return th, nil
}

// Verdict is the outcome of a window.
type Verdict string

const (
	VerdictPass Verdict = "PASS"
	VerdictFail Verdict = "FAIL"
	// VerdictRecordOnly means checks failed but thresholds are still being
	// calibrated, so the release isn't blocked.
	VerdictRecordOnly Verdict = "RECORD_ONLY"
)

// Check names the rule a finding broke.
type Check string

const (
	CheckNoData       Check = "NoData"
	CheckPeak         Check = "Peak"
	CheckSlope        Check = "Slope"
	CheckMedianGrowth Check = "MedianGrowth"
	CheckCountGrowth  Check = "CountGrowth"
	CheckRestarts     Check = "Restarts"
	CheckOOMKilled    Check = "OOMKilled"
	CheckPodReplaced  Check = "PodReplaced"
)

// Finding is one failed check.
type Finding struct {
	Node    string  `json:"node,omitempty"`
	Signal  string  `json:"signal,omitempty"`
	Check   Check   `json:"check"`
	Value   float64 `json:"value"`
	Limit   float64 `json:"limit"`
	Message string  `json:"message"`
}

// SignalSummary is the worst value of a signal across nodes, for calibration.
type SignalSummary struct {
	MaxPeak         float64 `json:"maxPeak"`
	MaxSlopePerHour float64 `json:"maxSlopePerHour,omitempty"`
}

// Result is the outcome of analysing one window.
type Result struct {
	Verdict     Verdict                  `json:"verdict"`
	WindowStart time.Time                `json:"windowStart"`
	WindowEnd   time.Time                `json:"windowEnd"`
	Nodes       int                      `json:"nodes"`
	Findings    []Finding                `json:"findings,omitempty"`
	Signals     map[string]SignalSummary `json:"signals"`
	// Notes record checks that were skipped, e.g. a node with too little
	// data for trend checks.
	Notes []string `json:"notes,omitempty"`
}

// Has reports whether any finding is for one of checks.
func (r Result) Has(checks ...Check) bool {
	for _, f := range r.Findings {
		if slices.Contains(checks, f.Check) {
			return true
		}
	}
	return false
}

type point struct {
	t time.Time
	v float64
}

// Analyze checks the samples taken in [start, end].
func Analyze(samples []collector.Sample, start, end time.Time, th Thresholds) Result {
	result := Result{WindowStart: start, WindowEnd: end, Signals: map[string]SignalSummary{}}

	byNode := map[string][]collector.Sample{}
	for _, s := range samples {
		if s.Time.Before(start) || s.Time.After(end) {
			continue
		}
		byNode[s.Node] = append(byNode[s.Node], s)
	}
	result.Nodes = len(byNode)
	if len(byNode) == 0 {
		result.Findings = append(result.Findings, Finding{Check: CheckNoData, Message: "no agent samples in the window"})
	}

	trendStart := start.Add(th.WarmUp.Duration)
	for _, node := range slices.Sorted(maps.Keys(byNode)) {
		nodeSamples := byNode[node]
		sort.Slice(nodeSamples, func(i, j int) bool { return nodeSamples[i].Time.Before(nodeSamples[j].Time) })
		result.checkNode(node, nodeSamples, trendStart, th)
	}

	switch {
	case len(result.Findings) == 0:
		result.Verdict = VerdictPass
	case th.RecordOnly:
		result.Verdict = VerdictRecordOnly
	default:
		result.Verdict = VerdictFail
	}
	return result
}

func (r *Result) checkNode(node string, samples []collector.Sample, trendStart time.Time, th Thresholds) {
	add := func(f Finding) {
		f.Node = node
		r.Findings = append(r.Findings, f)
	}

	// restarts: a restart, an OOM kill, or a new agent pod on the node all
	// reset the agent's memory, which would hide a leak and is a failure on
	// its own.
	pods := map[string]bool{}
	for _, s := range samples {
		pods[s.Pod] = true
	}
	if len(pods) > 1 {
		add(Finding{Check: CheckPodReplaced, Value: float64(len(pods)), Limit: 1,
			Message: fmt.Sprintf("agent pod was replaced during the window (%v)", slices.Sorted(maps.Keys(pods)))})
	}
	restarts, oomKills := restartsInWindow(samples)
	if restarts > th.MaxRestarts {
		add(Finding{Signal: collector.Restarts, Check: CheckRestarts, Value: float64(restarts), Limit: float64(th.MaxRestarts),
			Message: fmt.Sprintf("agent container restarted %d times", restarts)})
	}
	if oomKills > 0 {
		add(Finding{Signal: collector.OOMKilled, Check: CheckOOMKilled, Value: float64(oomKills), Limit: 0,
			Message: fmt.Sprintf("agent container was OOM killed %d times", oomKills)})
	}
	series := seriesBySignal(samples)

	// peaks cover the whole window, including warm-up.
	for _, signal := range slices.Sorted(maps.Keys(th.Peak)) {
		points := series[signal]
		if len(points) == 0 {
			continue
		}
		peak := maxValue(points)
		r.summarize(signal, func(s *SignalSummary) { s.MaxPeak = max(s.MaxPeak, peak) })
		limit := th.Peak[signal]
		if peak > limit.AsApproximateFloat64() {
			add(Finding{Signal: signal, Check: CheckPeak, Value: peak, Limit: limit.AsApproximateFloat64(),
				Message: fmt.Sprintf("%s peaked at %s, over %s", signal, format(signal, peak), limit.String())})
		}
	}

	// trend checks only look at post-warm-up samples, and need enough of them.
	trend := map[string][]point{}
	for signal, points := range series {
		i := sort.Search(len(points), func(i int) bool { return !points[i].t.Before(trendStart) })
		trend[signal] = points[i:]
	}
	if span := spanOf(trend); span < th.MinTrendWindow.Duration {
		r.Notes = append(r.Notes, fmt.Sprintf("%s: skipped trend checks, only %s of post-warm-up data (need %s)", node, span.Round(time.Second), th.MinTrendWindow.Duration))
		return
	}

	for _, signal := range slices.Sorted(maps.Keys(th.SlopePerHour)) {
		points := trend[signal]
		if len(points) < 2 {
			continue
		}
		slope := slopePerHour(points)
		r.summarize(signal, func(s *SignalSummary) { s.MaxSlopePerHour = max(s.MaxSlopePerHour, slope) })
		limit := th.SlopePerHour[signal]
		if slope > limit.AsApproximateFloat64() {
			add(Finding{Signal: signal, Check: CheckSlope, Value: slope, Limit: limit.AsApproximateFloat64(),
				Message: fmt.Sprintf("%s grew %s/h, over %s/h", signal, format(signal, slope), limit.String())})
		}
	}
	for _, signal := range slices.Sorted(maps.Keys(th.MedianGrowthPercent)) {
		first, last, ok := firstLastMedians(trend[signal], th.MedianWindow.Duration)
		if !ok || first <= 0 {
			continue
		}
		growth := (last - first) / first * 100
		if limit := th.MedianGrowthPercent[signal]; growth > limit {
			add(Finding{Signal: signal, Check: CheckMedianGrowth, Value: growth, Limit: limit,
				Message: fmt.Sprintf("%s median grew %.1f%% (%s to %s), over %.1f%%", signal, growth, format(signal, first), format(signal, last), limit)})
		}
	}
	for _, signal := range slices.Sorted(maps.Keys(th.CountGrowth)) {
		first, last, ok := firstLastMedians(trend[signal], th.MedianWindow.Duration)
		if !ok {
			continue
		}
		limit := th.CountGrowth[signal]
		if growth := last - first; growth > limit.AsApproximateFloat64() {
			add(Finding{Signal: signal, Check: CheckCountGrowth, Value: growth, Limit: limit.AsApproximateFloat64(),
				Message: fmt.Sprintf("%s median grew by %.0f (%.0f to %.0f), over %s", signal, growth, first, last, limit.String())})
		}
	}
}

func (r *Result) summarize(signal string, update func(*SignalSummary)) {
	s := r.Signals[signal]
	update(&s)
	r.Signals[signal] = s
}

// restartsInWindow counts the agent container restarts that happened during
// the samples, and how many of them were OOM kills. Restart counts are
// compared per pod between consecutive samples, so counts carried in from
// before the window, or reset by a pod replacement, aren't counted. The
// container's last termination reason stays OOMKilled until it terminates
// again, so a kill only counts on the sample where the restart count rose.
func restartsInWindow(samples []collector.Sample) (restarts, oomKills int) {
	last := map[string]float64{}
	for _, s := range samples {
		count, ok := s.Values[collector.Restarts]
		if !ok {
			continue
		}
		prev, seen := last[s.Pod]
		last[s.Pod] = count
		if !seen || count <= prev {
			continue
		}
		restarts += int(count - prev)
		if s.Values[collector.OOMKilled] > 0 {
			oomKills++
		}
	}
	return restarts, oomKills
}

func seriesBySignal(samples []collector.Sample) map[string][]point {
	series := map[string][]point{}
	for _, s := range samples {
		for signal, v := range s.Values {
			series[signal] = append(series[signal], point{s.Time, v})
		}
	}
	return series
}

// spanOf is the longest time covered by any signal's points.
func spanOf(series map[string][]point) time.Duration {
	var span time.Duration
	for _, points := range series {
		if len(points) > 1 {
			span = max(span, points[len(points)-1].t.Sub(points[0].t))
		}
	}
	return span
}

func maxValue(points []point) float64 {
	m := points[0].v
	for _, p := range points[1:] {
		m = max(m, p.v)
	}
	return m
}

// slopePerHour is the least-squares slope of points, in units per hour.
func slopePerHour(points []point) float64 {
	t0 := points[0].t
	var n, sumX, sumY, sumXY, sumXX float64
	for _, p := range points {
		x := p.t.Sub(t0).Hours()
		n++
		sumX += x
		sumY += p.v
		sumXY += x * p.v
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}

// firstLastMedians returns the medians of the first and last window of points.
func firstLastMedians(points []point, window time.Duration) (first, last float64, ok bool) {
	if len(points) < 2 {
		return 0, 0, false
	}
	var head, tail []float64
	for _, p := range points {
		if p.t.Sub(points[0].t) <= window {
			head = append(head, p.v)
		}
		if points[len(points)-1].t.Sub(p.t) <= window {
			tail = append(tail, p.v)
		}
	}
	return median(head), median(tail), true
}

func median(values []float64) float64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func format(signal string, v float64) string {
	if slices.Contains(collector.MemorySignals, signal) {
		return fmt.Sprintf("%.1fMi", v/(1<<20))
	}
	return fmt.Sprintf("%.0f", v)
}
