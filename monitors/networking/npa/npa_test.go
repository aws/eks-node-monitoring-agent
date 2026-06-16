package npa

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

	"context"
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

// mockManager implements monitor.Manager for unit tests. It records emitted
// conditions on the res channel and serves subscriptions from a BaseObserver.
type mockManager struct {
	err error
	obs observer.BaseObserver
	res chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.res <- condition
	return nil
}

func newTestManager() *mockManager {
	return &mockManager{
		obs: observer.BaseObserver{},
		res: make(chan monitor.Condition, 5),
	}
}

func newTestDetector(mgr *mockManager) *Detector {
	return New(mgr, logr.Discard())
}

func assertCondition(t *testing.T, mgr *mockManager, reason string, severity monitor.Severity) {
	t.Helper()
	select {
	case c := <-mgr.res:
		assert.Equal(t, reason, c.Reason)
		assert.Equal(t, severity, c.Severity)
	case <-time.After(time.Second):
		t.Fatalf("expected condition %q, but none was emitted", reason)
	}
}

func assertNoCondition(t *testing.T, mgr *mockManager) {
	t.Helper()
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no condition, but got %q", c.Reason)
	case <-time.After(50 * time.Millisecond):
		// no condition emitted — success
	}
}

// --- NPARepeatedlyRestart (checkRepeatedlyRestart) — Warning ---

func TestCheckRepeatedlyRestart(t *testing.T) {
	for _, tc := range []struct {
		name             string
		previousRestarts uint32
		currentRestarts  uint32
		expectCondition  bool
	}{
		{"DeltaAtThresholdTriggers", 0, 5, true},
		{"DeltaAboveThresholdTriggers", 10, 20, true},
		{"DeltaBelowThresholdNoTrigger", 0, 4, false},
		{"NoChangeNoTrigger", 7, 7, false},
		{"SingleRestartNoTrigger", 3, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager()
			d := newTestDetector(mgr)
			d.previousRestarts = tc.previousRestarts

			err := d.checkRepeatedlyRestart(tc.currentRestarts, "auto-restart", "signal")
			assert.NoError(t, err)

			assert.Equal(t, tc.currentRestarts, d.previousRestarts)

			if tc.expectCondition {
				assertCondition(t, mgr, "NPARepeatedlyRestart", monitor.SeverityWarning)
			} else {
				assertNoCondition(t, mgr)
			}
		})
	}
}

// --- NPANotRunning (checkNotRunning) — Fatal ---

func TestCheckNotRunning_FirstDetectionNoEmit(t *testing.T) {
	mgr := newTestManager()
	d := newTestDetector(mgr)

	err := d.checkNotRunning(false, "dead", "exit-code")
	assert.NoError(t, err)
	assert.False(t, d.npaNotRunningTime.IsZero(), "detection time should be recorded")
	assertNoCondition(t, mgr)
}

func TestCheckNotRunning_WithinWindowNoEmit(t *testing.T) {
	mgr := newTestManager()
	d := newTestDetector(mgr)
	d.npaNotRunningTime = time.Now().Add(-5 * time.Minute)

	err := d.checkNotRunning(false, "dead", "exit-code")
	assert.NoError(t, err)
	assertNoCondition(t, mgr)
}

func TestCheckNotRunning_AfterWindowEmits(t *testing.T) {
	mgr := newTestManager()
	d := newTestDetector(mgr)
	d.npaNotRunningTime = time.Now().Add(-16 * time.Minute)

	err := d.checkNotRunning(false, "dead", "exit-code")
	assert.NoError(t, err)
	assertCondition(t, mgr, "NPANotRunning", monitor.SeverityFatal)
}

func TestCheckNotRunning_ResetOnRecovery(t *testing.T) {
	mgr := newTestManager()
	d := newTestDetector(mgr)
	d.npaNotRunningTime = time.Now().Add(-16 * time.Minute)

	err := d.checkNotRunning(true, "running", "success")
	assert.NoError(t, err)
	assert.True(t, d.npaNotRunningTime.IsZero(), "detection time should reset when NPA is active")
	assertNoCondition(t, mgr)
}

// --- HandleLogs — all map to NPABPFRecoveryError (Warning) ---

func TestHandleLogs(t *testing.T) {
	for _, tc := range []struct {
		name            string
		line            string
		expectCondition bool
	}{
		// Recovery failures → NPABPFRecoveryError
		{"RecoveryFailedToRecover", "ERROR Failed to recover the BPF state", true},
		{"RecoveryStateRecoveryFailed", "BPF State Recovery failed error: something", true},
		// eBPF map errors → NPABPFRecoveryError
		{"GlobalMaps", "failed to recover global maps", true},
		{"IngressInMem", "got err for ingress in-mem map", true},
		{"EgressInMem", "got err for egress in-mem map", true},
		{"ClusterPolicyIngressInMem", "got err for cluster policy ingress in-mem map", true},
		{"ClusterPolicyEgressInMem", "got err for cluster policy egress in-mem map", true},
		// Non-matching → no condition
		{"RandomLine", "some random log line", false},
		{"StartupLine", "NPA started successfully", false},
		{"EmptyLine", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager()
			d := newTestDetector(mgr)

			err := d.HandleLogs(tc.line)
			assert.NoError(t, err)

			if tc.expectCondition {
				assertCondition(t, mgr, "NPABPFRecoveryError", monitor.SeverityWarning)
			} else {
				assertNoCondition(t, mgr)
			}
		})
	}
}
