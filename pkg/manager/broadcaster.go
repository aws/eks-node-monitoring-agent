package manager

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// Backstop spam-filter budget: twice the derived node ceiling, so that after
// the exporter's own shaping (which bounds emission to the ceiling per event
// type) the correlator can never drop an event first. The client-go default
// spam key is already node-scoped for this agent — one recorder source, one
// involved object, keyed per event type — so the backstop needs no custom
// key, and unlike a component-keyed spam filter it cannot be defeated by
// aggregation rebuilding events without their annotations.
const (
	backstopEventBurst = 2 * reasons.MaxComponents * componentEventBurst
	backstopEventQPS   = 2 * reasons.MaxComponents * componentEventQPS
)

// NewEventBroadcaster returns the event broadcaster for the agent's
// recorders. It customizes only two correlator behaviors, leaving the rest
// at client-go defaults:
//
//   - Aggregation groups per component: the default aggregation key would
//     collapse distinct messages from different components under the same
//     condition-type reason into one aggregate, swallowing the quieter
//     component's messages. The component annotation is part of the key, so
//     one component's chatter can only aggregate with itself. The key is
//     computed from each incoming event, where the annotation is intact.
//   - The spam filter becomes a generously sized backstop (see constants
//     above). Budget enforcement lives in the exporter's eventLimiter, where
//     the component is known and drops are observable; a correlator-level
//     budget would drop silently.
func NewEventBroadcaster() record.EventBroadcaster {
	return record.NewBroadcasterWithCorrelatorOptions(record.CorrelatorOptions{
		KeyFunc:   aggregationKeyFunc,
		BurstSize: backstopEventBurst,
		QPS:       backstopEventQPS,
	})
}

// aggregationKeyFunc is the client-go default aggregation key plus the
// owning-component annotation, so similar events aggregate only within
// their component.
func aggregationKeyFunc(event *corev1.Event) (string, string) {
	aggregateKey, localKey := record.EventAggregatorByReasonFunc(event)
	return strings.Join([]string{aggregateKey, event.Annotations[componentAnnotation]}, ""), localKey
}
