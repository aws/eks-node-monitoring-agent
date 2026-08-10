package manager

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestEventLimiter_ComponentIsolation is the acceptance test for the
// per-component budget: one component at worst-case flap drains only its own
// buckets, while another component's events still deliver immediately.
func TestEventLimiter_ComponentIsolation(t *testing.T) {
	l := newEventLimiter()

	// Component A floods Warnings: the burst delivers, the excess drops.
	delivered := 0
	for i := 0; i < componentEventBurst+10; i++ {
		if l.allow("ipamd", corev1.EventTypeWarning) {
			delivered++
		}
	}
	if delivered != componentEventBurst {
		t.Errorf("flooding component delivered %d Warnings, want exactly the burst %d", delivered, componentEventBurst)
	}

	// A's Warning bucket is empty now.
	if l.allow("ipamd", corev1.EventTypeWarning) {
		t.Error("flooding component's Warning must be rejected after its burst is drained")
	}

	// A's Normal budget is a separate bucket and still delivers (recovery
	// events must not compete with the failure Warnings).
	if !l.allow("ipamd", corev1.EventTypeNormal) {
		t.Error("flooding component's Normal bucket must be unaffected by its Warning flood")
	}

	// Component B is isolated: its Warning delivers immediately.
	if !l.allow("npa", corev1.EventTypeWarning) {
		t.Error("innocent component's Warning must deliver despite another component flooding")
	}
}

// TestEventLimiter_NodeCeilingUnreachableThroughComponentBuckets pins the
// bug-detector property: traffic that passes the per-component buckets can
// never fill the per-type node ceiling, because the ceiling equals the sum
// of the component budgets by construction.
func TestEventLimiter_NodeCeilingUnreachableThroughComponentBuckets(t *testing.T) {
	l := newEventLimiter()

	// Worst case: every declared component bursts fully, both event types.
	components := []string{"container-runtime", "ipamd", "kernel", "networking", "neuron", "npa", "nvidia", "storage", "diagnostics"}
	for _, c := range components {
		for _, eventType := range []string{corev1.EventTypeWarning, corev1.EventTypeNormal} {
			for i := 0; i < componentEventBurst+10; i++ {
				l.allow(c, eventType)
			}
		}
	}
	if got := testutil.ToFloat64(eventsCeilingRejected); got != 0 {
		t.Errorf("node ceiling rejected %v events under component-shaped worst case, want 0 (ceiling firing means an emitter bypassed the buckets)", got)
	}
}

// TestAggregationKeyFunc pins per-component aggregation: events differing
// only in their component annotation must never share an aggregate key,
// while events from the same component keep the default grouping.
func TestAggregationKeyFunc(t *testing.T) {
	event := func(component, message string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{componentAnnotation: component},
			},
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "test-node"},
			Source:         corev1.EventSource{Component: "eks-node-monitoring-agent"},
			Type:           corev1.EventTypeWarning,
			Reason:         "NetworkingReady",
			Message:        message,
		}
	}

	ipamdKey, _ := aggregationKeyFunc(event("ipamd", "IPAMDNotReady: a"))
	npaKey, _ := aggregationKeyFunc(event("npa", "NPANotRunning: b"))
	if ipamdKey == npaKey {
		t.Error("events from different components must not share an aggregate key")
	}

	ipamdKey2, localKey := aggregationKeyFunc(event("ipamd", "IPAMDNotReady: c"))
	if ipamdKey != ipamdKey2 {
		t.Error("events from the same component must share an aggregate key")
	}
	if localKey != "IPAMDNotReady: c" {
		t.Errorf("local key must stay the default (the message), got %q", localKey)
	}
}
