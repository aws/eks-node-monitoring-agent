// Package insights publishes the insights NMA detects in its Prometheus
// registry as the eks_insight gauge, in the same form node agents publish
// theirs:
//
//	eks_insight{type="NetworkAllowanceExceeded",reason="LinkLocal",resource_eni_id="eni-0123"} 1
//
// An insight's series is present while it's active, and removed once it
// clears.
package insights

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

const (
	metricName  = "eks_insight"
	help        = "An insight was detected. Only present while the insight is active."
	labelType   = "type"
	labelReason = "reason"
	// resourceLabelPrefix starts the label naming the resource an insight is
	// about.
	resourceLabelPrefix = "resource_"
)

// group is the set of insights one Replace swaps out: one kind of insight,
// from one monitor. The monitor is part of the identity but never a label, so
// that two monitors reporting the same kind do not clear each other, while
// still publishing it as one series.
type group struct {
	monitor string
	insight monitor.Insight
}

// Active is the set of active insights. It's a Prometheus collector: each
// active insight is one eks_insight series with value 1. Register it with a
// registry to publish it.
type Active struct {
	mu sync.Mutex
	// resourceValues holds the IDs of the resources with an active insight of
	// each group. A group with no IDs has no series.
	resourceValues map[group]map[string]struct{}
}

// New returns an empty set of active insights.
func New() *Active {
	return &Active{resourceValues: map[group]map[string]struct{}{}}
}

// Replace makes the given resources the only ones the monitor reports the
// insight for. A resource that had one and isn't in resourceValues has it
// cleared, and an empty set clears the monitor's whole set for that insight.
func (active *Active) Replace(monitorName string, insight monitor.Insight, resourceValues []string) {
	// a resource reported twice is still one insight, so the IDs are a set
	withInsight := make(map[string]struct{}, len(resourceValues))
	for _, resourceValue := range resourceValues {
		withInsight[resourceValue] = struct{}{}
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	active.resourceValues[group{monitor: monitorName, insight: insight}] = withInsight
}

// Describe sends no descriptors, which makes Active an unchecked collector:
// insights are about different resources, so their series have different
// labels.
func (active *Active) Describe(chan<- *prometheus.Desc) {}

// Collect sends one series per active insight.
func (active *Active) Collect(metrics chan<- prometheus.Metric) {
	for _, metric := range active.snapshot() {
		metrics <- metric
	}
}

// snapshot builds the series under the lock and returns them, so that Collect
// never holds the lock while sending on a channel.
func (active *Active) snapshot() []prometheus.Metric {
	active.mu.Lock()
	defer active.mu.Unlock()
	var snapshot []prometheus.Metric
	sent := map[string]struct{}{}
	for insightGroup, resourceValues := range active.resourceValues {
		insight := insightGroup.insight
		names := []string{labelType, labelReason}
		if insight.ResourceName != "" {
			names = append(names, resourceLabelPrefix+insight.ResourceName)
		}
		for resourceValue := range resourceValues {
			values := []string{insight.Type, insight.Reason}
			if insight.ResourceName != "" {
				values = append(values, resourceValue)
			}
			// the label names follow from the resource name, so the names and
			// values together identify the series
			seriesKey := strings.Join(append([]string{insight.ResourceName}, values...), ",")
			if _, duplicate := sent[seriesKey]; duplicate {
				continue
			}
			sent[seriesKey] = struct{}{}

			desc := prometheus.NewDesc(metricName, help, names, nil)
			metric, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, 1, values...)
			if err != nil {
				// the rest of the scrape is still worth serving
				continue
			}
			snapshot = append(snapshot, metric)
		}
	}
	return snapshot
}
