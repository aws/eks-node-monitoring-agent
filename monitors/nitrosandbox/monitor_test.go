package nitrosandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

type mockManager struct {
	res []monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return make(chan string), nil
}

func (m *mockManager) Notify(_ context.Context, condition monitor.Condition) error {
	m.res = append(m.res, condition)
	return nil
}

// testMonitor builds a monitor whose view of the node is entirely injected: an
// EKS Auto node with sandbox hardware, a healthy unit and a live socket. Each
// test overrides only the piece it is about.
func testMonitor(mgr monitor.Manager, status unitStatus) *nitroSandboxMonitor {
	return &nitroSandboxMonitor{
		manager:    mgr,
		tags:       func() []string { return []string{config.EKSAuto, config.Bottlerocket} },
		unitStatus: func(context.Context, string) (unitStatus, error) { return status, nil },
		dialSocket: func(string) error { return nil },
		fileExists: func(string) bool { return true },
	}
}

func healthy() unitStatus {
	return unitStatus{activeState: "active", result: "success"}
}

// checkTwice runs the poll twice, because a single bad poll is deliberately not
// reported: the condition it would set never recovers while the agent runs.
func checkTwice(t *testing.T, m *nitroSandboxMonitor) {
	t.Helper()
	require.NoError(t, m.checkCRIProxy(context.Background()))
	require.NoError(t, m.checkCRIProxy(context.Background()))
}

func TestCheckCRIProxy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     unitStatus
		mutate     func(*nitroSandboxMonitor)
		wantReport bool
	}{
		{
			name:   "healthy unit with a live socket reports nothing",
			status: healthy(),
		},
		{
			name:       "failed unit is reported",
			status:     unitStatus{activeState: "failed", result: "exit-code"},
			wantReport: true,
		},
		{
			name:       "inactive unit is reported",
			status:     unitStatus{activeState: "inactive", result: "success"},
			wantReport: true,
		},
		{
			name: "unit skipped by its own ExecCondition is not a fault",
			// Sandbox support is switched off on this node; the unit is skipped
			// rather than started, so reporting it would flag a healthy node.
			status: unitStatus{activeState: "inactive", result: execConditionResult},
		},
		{
			name:   "unreachable socket under an active unit is reported",
			status: healthy(),
			mutate: func(m *nitroSandboxMonitor) {
				m.dialSocket = func(string) error { return errors.New("connection refused") }
			},
			wantReport: true,
		},
		{
			name:   "node without sandbox hardware reports nothing",
			status: unitStatus{activeState: "failed", result: "exit-code"},
			mutate: func(m *nitroSandboxMonitor) {
				m.fileExists = func(string) bool { return false }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &mockManager{}
			m := testMonitor(mgr, tc.status)
			if tc.mutate != nil {
				tc.mutate(m)
			}

			checkTwice(t, m)

			if !tc.wantReport {
				assert.Empty(t, mgr.res)
				return
			}
			require.NotEmpty(t, mgr.res)
			assert.Equal(t, "CRIProxyUnhealthy", mgr.res[0].Reason)
			assert.Equal(t, monitor.SeverityFatal, mgr.res[0].Severity)
			assert.NotEmpty(t, mgr.res[0].Message)
		})
	}
}

// A Fatal condition never returns to True while the agent runs, so one bad poll
// must not condemn a node: a containerd restart takes the proxy down with it and
// it rebinds a second later.
func TestCheckCRIProxyRequiresTwoBadPolls(t *testing.T) {
	mgr := &mockManager{}
	m := testMonitor(mgr, unitStatus{activeState: "failed", result: "exit-code"})

	require.NoError(t, m.checkCRIProxy(context.Background()))
	assert.Empty(t, mgr.res, "first bad poll should not report")

	require.NoError(t, m.checkCRIProxy(context.Background()))
	assert.Len(t, mgr.res, 1, "second consecutive bad poll should report")
}

func TestCheckCRIProxyRecoveryResetsTheCount(t *testing.T) {
	mgr := &mockManager{}
	status := unitStatus{activeState: "failed", result: "exit-code"}
	m := testMonitor(mgr, healthy())
	m.unitStatus = func(context.Context, string) (unitStatus, error) { return status, nil }

	require.NoError(t, m.checkCRIProxy(context.Background()))
	status = healthy()
	require.NoError(t, m.checkCRIProxy(context.Background()))
	status = unitStatus{activeState: "failed", result: "exit-code"}
	require.NoError(t, m.checkCRIProxy(context.Background()))

	assert.Empty(t, mgr.res, "a recovery between bad polls should reset the count")
}

// Restart=always keeps a crash-looping unit in ActiveState=active, so the
// restart count is the only evidence.
func TestCheckCRIProxyRestartLoop(t *testing.T) {
	mgr := &mockManager{}
	status := unitStatus{activeState: "active", result: "success", restarts: 7}
	m := testMonitor(mgr, status)
	m.unitStatus = func(context.Context, string) (unitStatus, error) { return status, nil }

	// First poll establishes the baseline: restarts predating the agent are not
	// this agent's news to report.
	require.NoError(t, m.checkCRIProxy(context.Background()))
	assert.Empty(t, mgr.res, "baseline poll should not report")

	status.restarts = 8
	require.NoError(t, m.checkCRIProxy(context.Background()))
	assert.Empty(t, mgr.res, "one observed restart is not yet reported")

	status.restarts = 9
	require.NoError(t, m.checkCRIProxy(context.Background()))
	require.Len(t, mgr.res, 1)
	assert.Contains(t, mgr.res[0].Message, "restarted")
}

func TestCheckCRIProxyUnreadableUnitStateErrors(t *testing.T) {
	mgr := &mockManager{}
	m := testMonitor(mgr, healthy())
	m.unitStatus = func(context.Context, string) (unitStatus, error) {
		return unitStatus{}, errors.New("no dbus")
	}

	assert.Error(t, m.checkCRIProxy(context.Background()))
	assert.Empty(t, mgr.res, "a dbus failure must not be reported as a node fault")
}

func TestRegisterIsInertOffEKSAuto(t *testing.T) {
	mgr := &mockManager{}
	m := testMonitor(mgr, unitStatus{activeState: "failed", result: "exit-code"})
	m.tags = func() []string { return []string{config.Bottlerocket} }

	require.NoError(t, m.Register(context.Background(), mgr))
	assert.False(t, m.activatedByTags())
	assert.Empty(t, mgr.res)
}

func TestActivatedByTags(t *testing.T) {
	m := testMonitor(&mockManager{}, healthy())

	m.tags = func() []string { return []string{config.EKSAuto} }
	assert.True(t, m.activatedByTags())

	m.tags = func() []string { return []string{config.Hybrid, config.UbuntuDistro} }
	assert.False(t, m.activatedByTags())
}

func TestSandboxSupported(t *testing.T) {
	m := testMonitor(&mockManager{}, healthy())
	assert.True(t, m.sandboxSupported())

	m.fileExists = func(path string) bool { return path == enclaveDevice }
	assert.False(t, m.sandboxSupported(), "both the device and the slot attribute are required")
}
