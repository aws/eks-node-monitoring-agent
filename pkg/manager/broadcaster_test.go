package manager

import (
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

// countingSink counts every event write the correlator lets through.
type countingSink struct {
	ops atomic.Int64
}

func (s *countingSink) Create(event *corev1.Event) (*corev1.Event, error) {
	s.ops.Add(1)
	return event, nil
}

func (s *countingSink) Update(event *corev1.Event) (*corev1.Event, error) {
	s.ops.Add(1)
	return event, nil
}

func (s *countingSink) Patch(event *corev1.Event, data []byte) (*corev1.Event, error) {
	s.ops.Add(1)
	return event, nil
}

// TestBroadcasterBackstopNeverFiresBelowExporterCeiling drives the real
// client-go correlator, configured exactly as production, with worst-case
// exporter-shaped traffic: every declared component bursting its full
// per-type budget with distinct messages. The exporter's shaping bounds this
// traffic to the node ceiling per event type, and the correlator's spam
// filter is sized at twice that ceiling, so not one event may be skipped.
//
// This test is the designated tripwire for k8s.io/client-go bumps: the
// backstop-sizing argument depends on correlator internals (default spam key
// composition, aggregation before filtering) verified at v0.36.2. If a bump
// changes them, this fails before production does.
func TestBroadcasterBackstopNeverFiresBelowExporterCeiling(t *testing.T) {
	sink := &countingSink{}
	broadcaster := NewEventBroadcaster()
	defer broadcaster.Shutdown()
	watch := broadcaster.StartRecordingToSink(sink)
	defer watch.Stop()

	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "eks-node-monitoring-agent"})
	nodeRef := &corev1.ObjectReference{Kind: "Node", Name: "test-node", APIVersion: "v1"}

	components := []string{"container-runtime", "ipamd", "kernel", "networking", "neuron", "npa", "nvidia", "storage", "diagnostics"}
	emitted := 0
	for _, component := range components {
		for _, eventType := range []string{corev1.EventTypeWarning, corev1.EventTypeNormal} {
			for i := 0; i < componentEventBurst; i++ {
				recorder.AnnotatedEventf(nodeRef,
					map[string]string{componentAnnotation: component},
					eventType, "NetworkingReady", "SomeReason: distinct failure detail %d for %s", i, component)
				emitted++
			}
		}
	}

	// Every emitted event must reach the sink as a create or patch; a
	// shortfall means the correlator skipped events below the exporter's
	// ceiling. The queue (1000) is comfortably above the emitted count, so
	// queue drops cannot explain a shortfall either.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if int(sink.ops.Load()) == emitted {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sink received %d of %d events within deadline: the correlator backstop dropped exporter-shaped traffic", sink.ops.Load(), emitted)
}
