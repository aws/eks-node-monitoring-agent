package probe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

// fakeManager records notified conditions.
type fakeManager struct {
	conditions []monitor.Condition
}

func (m *fakeManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return nil, nil
}

func (m *fakeManager) Notify(_ context.Context, c monitor.Condition) error {
	m.conditions = append(m.conditions, c)
	return nil
}

// scriptedTransport returns canned results in sequence, repeating the last.
type scriptedTransport struct {
	results []Result
	i       int
}

func (t *scriptedTransport) Do(context.Context, probe.Check) Result {
	r := t.results[min(t.i, len(t.results)-1)]
	t.i++
	return r
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func newTestRunner(t *testing.T, spec probe.Spec, mgr *fakeManager, results ...Result) *Runner {
	t.Helper()
	r, err := NewRunner(spec, mgr, logr.Discard())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r.transports[spec.Checks.Liveness.Transport] = &scriptedTransport{results: results}
	r.startedAt = time.Now() // grace period baseline for direct runOnce calls
	return r
}

func runnerSpec(threshold int, grace time.Duration) probe.Spec {
	return probe.Spec{
		Subsystem: "ipamd",
		Checks: probe.Checks{
			Liveness: probe.Check{Transport: probe.TransportSystemdDBus, Address: "ipamd.service"},
		},
		ReasonOnFail:       "IPAMDNotRunning",
		FailureSeverity:    monitor.SeverityFatal,
		Interval:           metav1.Duration{Duration: 30 * time.Second},
		FailureThreshold:   threshold,
		StartupGracePeriod: metav1.Duration{Duration: grace},
	}
}

func healthy() Result   { return Result{Outcome: OutcomeHealthy} }
func unhealthy() Result { return Result{Outcome: OutcomeUnhealthy, Detail: "ActiveState=failed"} }
func unknown() Result   { return Result{Outcome: OutcomeUnknown, Detail: "dbus down"} }

func TestRunner_EmitsAfterConsecutiveFailures(t *testing.T) {
	mgr := &fakeManager{}
	r := newTestRunner(t, runnerSpec(3, 0), mgr, unhealthy())

	for i := 0; i < 2; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 0 {
		t.Fatalf("emitted before threshold: %+v", mgr.conditions)
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected 1 condition after threshold, got %d", len(mgr.conditions))
	}
	c := mgr.conditions[0]
	if c.Reason != "IPAMDNotRunning" || c.Severity != monitor.SeverityFatal || c.Resolved {
		t.Errorf("unexpected condition: %+v", c)
	}
	if !strings.Contains(c.Message, "ipamd liveness check") || !strings.Contains(c.Message, "ActiveState=failed") {
		t.Errorf("message should identify the check and detail, got %q", c.Message)
	}
}

func TestRunner_SuccessResetsCounter(t *testing.T) {
	mgr := &fakeManager{}
	// U U H U U — never 3 consecutive, so nothing may be emitted.
	r := newTestRunner(t, runnerSpec(3, 0), mgr, unhealthy(), unhealthy(), healthy(), unhealthy(), unhealthy())

	for i := 0; i < 5; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 0 {
		t.Fatalf("expected no emission without 3 consecutive failures, got %+v", mgr.conditions)
	}
}

func TestRunner_RecoveryEmitsResolvedCondition(t *testing.T) {
	mgr := &fakeManager{}
	r := newTestRunner(t, runnerSpec(2, 0), mgr, unhealthy(), unhealthy(), healthy(), healthy())

	for i := 0; i < 4; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Expect: failure at tick 2, resolution at tick 3, nothing at tick 4.
	if len(mgr.conditions) != 2 {
		t.Fatalf("expected exactly failure+resolution, got %+v", mgr.conditions)
	}
	if mgr.conditions[0].Resolved {
		t.Error("first condition should be the failure")
	}
	c := mgr.conditions[1]
	if !c.Resolved || c.Reason != "IPAMDNotRunning" {
		t.Errorf("second condition should resolve the reason: %+v", c)
	}
}

func TestRunner_UnknownDoesNotCountOrReset(t *testing.T) {
	mgr := &fakeManager{}
	// U U ? U — unknown neither increments nor resets, so the 4th tick is the
	// 3rd consecutive failure and emits.
	r := newTestRunner(t, runnerSpec(3, 0), mgr, unhealthy(), unhealthy(), unknown(), unhealthy())

	for i := 0; i < 3; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 0 {
		t.Fatalf("unknown tick must not emit: %+v", mgr.conditions)
	}
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected emission on 3rd consecutive failure after unknown, got %d", len(mgr.conditions))
	}
}

func TestRunner_StartupGraceSuppressesEmission(t *testing.T) {
	mgr := &fakeManager{}
	r := newTestRunner(t, runnerSpec(1, 5*time.Minute), mgr, unhealthy())

	// Within grace: failures counted but suppressed.
	for i := 0; i < 3; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 0 {
		t.Fatalf("expected suppression during grace period, got %+v", mgr.conditions)
	}

	// After grace: the accumulated failures emit on the next cycle.
	r.now = func() time.Time { return r.startedAt.Add(10 * time.Minute) }
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected emission after grace period, got %d", len(mgr.conditions))
	}
}

func TestRunner_ReadinessFailureCounts(t *testing.T) {
	mgr := &fakeManager{}
	spec := runnerSpec(1, 0)
	spec.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/healthz"}
	spec.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz"}

	r, err := NewRunner(spec, mgr, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	r.startedAt = time.Now()
	// Liveness healthy, readiness unhealthy.
	r.transports[probe.TransportHTTPLoopback] = &scriptedTransport{results: []Result{
		healthy(), {Outcome: OutcomeUnhealthy, Detail: "GET /readyz: 503"},
	}}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected readiness failure to emit, got %d", len(mgr.conditions))
	}
	if !strings.Contains(mgr.conditions[0].Message, "readiness check") {
		t.Errorf("message should attribute the readiness check, got %q", mgr.conditions[0].Message)
	}
}

func TestRunner_StartExitsOnContextCancel(t *testing.T) {
	mgr := &fakeManager{}
	r := newTestRunner(t, runnerSpec(1, 0), mgr, healthy())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not exit on context cancellation")
	}
}
