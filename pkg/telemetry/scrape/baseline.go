package scrape

import (
	"sort"
	"strings"
	"time"
)

const evictAfter = 3

type Baselines struct {
	series      map[string]*baseline
	unreachable int
}

type baseline struct {
	value     float64
	start     time.Time
	missing   int
	next      *float64
	nextStart time.Time
}

type Delta struct {
	Metric string
	Series Series
	Value  float64
	Start  time.Time
}

func NewBaselines() *Baselines {
	return &Baselines{series: map[string]*baseline{}}
}

func (b *Baselines) Observe(now time.Time, counters map[string][]Series) []Delta {
	b.unreachable = 0
	seen := map[string]bool{}
	var out []Delta
	for metric, series := range counters {
		for _, s := range series {
			key := seriesKey(metric, s.Labels)
			seen[key] = true
			base, ok := b.series[key]
			// TODO: also reset when process_start_time_seconds shows the
			// agent restarted.
			if !ok || s.Value < base.value {
				b.series[key] = &baseline{value: s.Value, start: now}
				continue
			}
			base.missing = 0
			v := s.Value
			base.next, base.nextStart = &v, now
			out = append(out, Delta{Metric: metric, Series: s, Value: s.Value - base.value, Start: base.start})
		}
	}
	for key, base := range b.series {
		if seen[key] {
			continue
		}
		base.next = nil
		if base.missing++; base.missing >= evictAfter {
			// TODO: count evicted baselines.
			delete(b.series, key)
		}
	}
	return out
}

func (b *Baselines) Advance() {
	for _, base := range b.series {
		if base.next != nil {
			base.value, base.start, base.next = *base.next, base.nextStart, nil
		}
	}
}

func (b *Baselines) Unreachable() {
	if b.unreachable++; b.unreachable >= evictAfter {
		// TODO: count evicted baselines.
		b.series = map[string]*baseline{}
	}
}

func seriesKey(metric string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(metric)
	for _, k := range keys {
		sb.WriteString("\x00" + k + "=" + labels[k])
	}
	return sb.String()
}
