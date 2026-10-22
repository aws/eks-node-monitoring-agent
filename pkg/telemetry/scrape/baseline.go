package scrape

import (
	"maps"
	"slices"
	"strings"
	"time"
)

const evictAfter = 3

// Baselines tracks one target's counter values, so counters can be sent as
// deltas.
type Baselines struct {
	series        map[string]*baseline
	failedScrapes int
}

type baseline struct {
	// acked is the counter value deltas are measured from: the value in the
	// last report that was accepted or dropped, or the value after a reset.
	acked float64
	// start is when acked was scraped, sent as the delta's start time.
	start time.Time
	// lastScraped is the most recently scraped value, used to detect resets.
	// Unlike next, it's kept while the series is absent.
	lastScraped float64
	// carry is the increase up to a reset that no report has acknowledged
	// yet. It's added to every delta until the next Advance.
	carry float64
	// missing is how many scrapes in a row the series has been absent from.
	missing int
	// next is the value from the latest scrape, which becomes acked on the
	// next Advance. It's nil if the series was absent from the latest scrape.
	next *float64
	// nextStart is when next was scraped, and becomes start on Advance.
	nextStart time.Time
}

// Delta is a counter series' increase since Start.
type Delta struct {
	Metric string
	Series Series
	Value  float64
	Start  time.Time
}

// NewBaselines returns empty Baselines.
func NewBaselines() *Baselines {
	return &Baselines{series: map[string]*baseline{}}
}

// Observe takes the counter series from one successful scrape, taken at time "now",
// and returns each series' increase since its baseline. A series' baseline is
// its value in the last report that was sent and accepted or dropped, so a report
// that's held leaves baselines in place and the next delta covers the gap.
//
// For each series in counters:
//   - Seen for the first time: it gets a baseline at its current value and no
//     delta this time.
//   - Lower than its last observed value: the counter reset. Its current value
//     becomes the new baseline, adding nothing for this interval. The increase
//     from the old baseline up to the last observed value hasn't been
//     acknowledged yet, so it's carried over; with nothing carried, there's no
//     delta this time.
//   - Otherwise: it returns the carried increase plus the increase over the
//     baseline, starting at the baseline's time, and records the current value
//     as pending, to become the new baseline on the next Advance.
//
// Series that have a baseline but are missing from counters lose their pending
// value, and are evicted after missing evictAfter scrapes in a row.
func (baselines *Baselines) Observe(now time.Time, counters map[string][]Series) []Delta {
	baselines.failedScrapes = 0
	seen := map[string]bool{}
	var out []Delta
	for metric, series := range counters {
		for _, sample := range series {
			key := seriesKey(metric, sample.Labels)
			seen[key] = true
			base, ok := baselines.series[key]
			// TODO: also reset when process_start_time_seconds shows the
			// agent restarted.
			if !ok {
				baselines.series[key] = &baseline{acked: sample.Value, lastScraped: sample.Value, start: now}
				continue
			}
			base.missing = 0
			if sample.Value < base.lastScraped {
				base.carry += base.lastScraped - base.acked
				base.acked = sample.Value
				if base.carry == 0 {
					*base = baseline{acked: sample.Value, lastScraped: sample.Value, start: now}
					continue
				}
			}
			base.lastScraped = sample.Value
			current := sample.Value
			base.next, base.nextStart = &current, now
			out = append(out, Delta{Metric: metric, Series: sample, Value: base.carry + sample.Value - base.acked, Start: base.start})
		}
	}
	for key, base := range baselines.series {
		if seen[key] {
			continue
		}
		base.next = nil
		if base.missing++; base.missing >= evictAfter {
			// TODO: count evicted baselines.
			delete(baselines.series, key)
		}
	}
	return out
}

// Advance makes each series' pending value from the last Observe its baseline.
// Should be called once reports are Accepted or Dropped.
func (baselines *Baselines) Advance() {
	for _, base := range baselines.series {
		if base.next != nil {
			base.acked, base.start, base.carry, base.next = *base.next, base.nextStart, 0, nil
		}
	}
}

// ScrapeFailed records a failed scrape of the target: such as a timeout, refused connection,
// error status, oversized response, parse error or identity mismatch. After evictAfter failures
// in a row, every baseline is evicted: a target that has been down that long has most likely
// restarted or stopped, so its counters start over, and a target that's gone for good stops
// holding memory. A successful scrape is expected to reset the count.
func (baselines *Baselines) ScrapeFailed() {
	if baselines.failedScrapes++; baselines.failedScrapes >= evictAfter {
		// TODO: count evicted baselines.
		baselines.series = map[string]*baseline{}
	}
}

// seriesKey builds a map key that identifies a series by its metric name and
// label set, for example "foo_total\x00api=Describe\x00code=500". Labels are
// sorted by name, so the same label set always gives the same key regardless
// of map order. Each label pair is prefixed with a NUL byte, which can't
// appear in metric or label names, so the label boundaries can't be confused
// with "=" or "," inside label values.
func seriesKey(metric string, labels map[string]string) string {
	var stringBuilder strings.Builder
	stringBuilder.WriteString(metric)
	for _, labelName := range slices.Sorted(maps.Keys(labels)) {
		stringBuilder.WriteString("\x00" + labelName + "=" + labels[labelName])
	}
	return stringBuilder.String()
}
