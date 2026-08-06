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
	// U U H U U — never 3 consecutive, so the failure condition may not be
	// emitted. The completed below-threshold episode (U U H) emits exactly
	// one Warning summary; the trailing in-progress streak emits nothing.
	r := newTestRunner(t, runnerSpec(3, 0), mgr, unhealthy(), unhealthy(), healthy(), unhealthy(), unhealthy())

	for i := 0; i < 5; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected exactly the episode summary, got %+v", mgr.conditions)
	}
	c := mgr.conditions[0]
	if c.Severity != monitor.SeverityWarning || c.Resolved {
		t.Errorf("episode summary must be a non-resolved Warning: %+v", c)
	}
	if c.Reason != "IPAMDNotRunning" {
		t.Errorf("episode summary must use the probe's reason, got %q", c.Reason)
	}
	if !strings.Contains(c.Message, "ipamd liveness check") || !strings.Contains(c.Message, "2 consecutive checks") || !strings.Contains(c.Message, "recovered") {
		t.Errorf("episode summary message should carry check, streak length, and recovery, got %q", c.Message)
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

func TestRunner_RecoveryHysteresis(t *testing.T) {
	mgr := &fakeManager{}
	spec := runnerSpec(3, 0)
	spec.RecoveryThreshold = 2
	// U U U → failure fires; H counts 1 of 2 healthy; U resets the recovery
	// counter (below the failure threshold, so no re-notification, and no
	// episode summary while the failure is fired); H counts 1; H counts 2 →
	// resolution.
	r := newTestRunner(t, spec, mgr,
		unhealthy(), unhealthy(), unhealthy(), healthy(), unhealthy(), healthy(), healthy())

	for i := 0; i < 6; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// After 6 ticks: only the failure. A premature resolution here means the
	// mid-recovery failure did not reset the counter; a Warning here means an
	// episode summary was emitted while the failure was fired.
	if len(mgr.conditions) != 1 || mgr.conditions[0].Resolved {
		t.Fatalf("expected only the fired failure after 6 ticks, got %+v", mgr.conditions)
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mgr.conditions) != 2 || !mgr.conditions[1].Resolved {
		t.Fatalf("expected resolution on 2nd consecutive healthy tick, got %+v", mgr.conditions)
	}
}

func TestRunner_UnknownDoesNotCountTowardRecovery(t *testing.T) {
	mgr := &fakeManager{}
	spec := runnerSpec(1, 0)
	spec.RecoveryThreshold = 2
	// U → failure fires; H counts 1 of 2; ? leaves the recovery counter
	// untouched; H counts 2 → resolution.
	r := newTestRunner(t, spec, mgr, unhealthy(), healthy(), unknown(), healthy())

	for i := 0; i < 3; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// A resolution here would mean the unknown tick counted as healthy.
	if len(mgr.conditions) != 1 {
		t.Fatalf("expected only the fired failure after unknown tick, got %+v", mgr.conditions)
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A missing resolution here would mean the unknown tick reset the counter.
	if len(mgr.conditions) != 2 || !mgr.conditions[1].Resolved {
		t.Fatalf("expected resolution on 2nd countable healthy tick, got %+v", mgr.conditions)
	}
}

func TestRunner_EpisodeInGraceEmitsNoSummary(t *testing.T) {
	mgr := &fakeManager{}
	r := newTestRunner(t, runnerSpec(3, 5*time.Minute), mgr,
		unhealthy(), unhealthy(), healthy(), unhealthy(), healthy())

	// U U H inside grace: a completed below-threshold episode, but it began
	// during startup grace — boot noise, no summary.
	for i := 0; i < 3; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 0 {
		t.Fatalf("episode that began in grace must not emit, got %+v", mgr.conditions)
	}

	// Past grace: U H is a completed below-threshold episode — one summary.
	r.now = func() time.Time { return r.startedAt.Add(10 * time.Minute) }
	for i := 0; i < 2; i++ {
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.conditions) != 1 || mgr.conditions[0].Severity != monitor.SeverityWarning {
		t.Fatalf("expected exactly one summary Warning for the post-grace episode, got %+v", mgr.conditions)
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
