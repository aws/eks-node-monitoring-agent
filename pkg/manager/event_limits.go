package manager

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// Per-component event quota Q: a 25-event burst refilled at one token per
// 300 seconds (~12/hour), per node, per event type. The component — the
// owning agent or monitor from the reasons.yaml ledger — is the unit of the
// budget, so one flooding component starves only itself. These are working
// numbers pending the scale test; see
// .kiro/specs/probe-framework-event-limits-spec.md section 2.
const (
	componentEventBurst = 25
	componentEventQPS   = 1.0 / 300.0
)

var (
	eventsEmitted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nma_events_emitted_total",
			Help: "Events recorded to the Kubernetes API, per component and event type.",
		},
		[]string{"component", "type"},
	)
	eventsDropped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nma_events_dropped_total",
			Help: "Events dropped by the per-component budget, per component and event type.",
		},
		[]string{"component", "type"},
	)
	eventsCeilingRejected = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "nma_events_node_ceiling_rejected_total",
			Help: "Events rejected by the derived node-global ceiling. The ceiling equals the sum of the per-component budgets, so any rejection means an emitter bypassed the component buckets — alarm on it as a bug.",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		eventsEmitted,
		eventsDropped,
		eventsCeilingRejected,
	)
}

// eventLimiter shapes event emission ahead of the recorder: one token bucket
// per (component, event type) as the guaranteed budget, and one node-global
// bucket per event type, derived as MaxComponents times the component quota,
// as the bug detector. Enforcement lives here, in the exporter path, rather
// than in the client-go correlator: the correlator's spam-filter key cannot
// carry the component through aggregation (aggregated events are rebuilt
// without annotations), and its drops are silent, while drops here are
// observable per component.
type eventLimiter struct {
	mu sync.Mutex
	// componentBuckets is keyed by component + "/" + event type. Keys are
	// bounded: components are validated against the reasons.yaml ledger at
	// startup, and event types are Normal or Warning.
	componentBuckets map[string]flowcontrol.RateLimiter
	// nodeBuckets is keyed by event type. Per type, the ceiling equals the
	// sum of the per-component budgets by construction, so it cannot fill
	// during legitimate operation.
	nodeBuckets map[string]flowcontrol.RateLimiter
}

func newEventLimiter() *eventLimiter {
	return &eventLimiter{
		componentBuckets: make(map[string]flowcontrol.RateLimiter),
		nodeBuckets:      make(map[string]flowcontrol.RateLimiter),
	}
}

// allow reports whether one event of the given type may be recorded for the
// component, consuming budget. Component-budget rejections increment the
// per-component drop counter; node-ceiling rejections increment the
// emitter-bug counter.
func (l *eventLimiter) allow(component, eventType string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.componentBuckets[component+"/"+eventType]
	if !ok {
		bucket = flowcontrol.NewTokenBucketRateLimiter(componentEventQPS, componentEventBurst)
		l.componentBuckets[component+"/"+eventType] = bucket
	}
	if !bucket.TryAccept() {
		eventsDropped.WithLabelValues(component, eventType).Inc()
		return false
	}

	node, ok := l.nodeBuckets[eventType]
	if !ok {
		node = flowcontrol.NewTokenBucketRateLimiter(reasons.MaxComponents*componentEventQPS, reasons.MaxComponents*componentEventBurst)
		l.nodeBuckets[eventType] = node
	}
	if !node.TryAccept() {
		eventsCeilingRejected.Inc()
		return false
	}

	eventsEmitted.WithLabelValues(component, eventType).Inc()
	return true
}
