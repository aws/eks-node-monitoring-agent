//go:build !darwin

package nvidia_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

type mockSysInfo struct{}

func (*mockSysInfo) Arch() string {
	return "mock"
}

type mockManager struct {
	monitor.Manager
	obs     observer.BaseObserver
	results chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), nil
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.results <- condition
	return nil
}

// immediateTick returns a TickFunc that fires immediately and then at short
// intervals, eliminating the jitter delay that makes timer-based tests slow.
func immediateTick(ctx context.Context, _ time.Duration) <-chan time.Time {
	ticker := time.NewTicker(50 * time.Millisecond)
	go func() {
		<-ctx.Done()
		ticker.Stop()
	}()
	return ticker.C
}

func newMonitorWithDcgm() (monitor.Monitor, *fake.FakeDcgm) {
	mockDcgm := &fake.FakeDcgm{
		PolicyChan: make(chan dcgmapi.PolicyViolation, 5),
	}
	nvidiaMonitor := nvidia.NewNvidiaMonitorWithDeps(mockDcgm, &mockSysInfo{}, immediateTick)
	return nvidiaMonitor, mockDcgm
}

func newMockManager() *mockManager {
	return &mockManager{
		results: make(chan monitor.Condition, 5),
	}
}

// awaitCondition waits for a condition from the mock manager with a timeout.
func awaitCondition(t *testing.T, mgr *mockManager, timeout time.Duration) monitor.Condition {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return monitor.Condition{}
	case result := <-mgr.results:
		return result
	}
}

func TestNvidiaMonitor(t *testing.T) {
	const testTimeout = 5 * time.Second

	t.Run("WellKnownXIDCode", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)
		xidCode := uint(13)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{
			Condition: dcgmapi.XidPolicy,
			Data:      dcgmapi.XidPolicyCondition{ErrNum: xidCode},
		}

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, fmt.Sprintf("NvidiaXID%dError", xidCode), result.Reason)
	})

	t.Run("UnknownXIDCode", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)
		xidCode := uint(20)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{
			Condition: dcgmapi.XidPolicy,
			Data:      dcgmapi.XidPolicyCondition{ErrNum: xidCode},
		}

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, fmt.Sprintf("NvidiaXID%dWarning", xidCode), result.Reason)
	})

	t.Run("DoubleBitError", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{
			Condition: dcgmapi.DbePolicy,
			Data:      dcgmapi.DbePolicyCondition{Location: "foo", NumErrors: 5},
		}

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, "NvidiaDoubleBitError", result.Reason)
	})

	t.Run("NVLinkFinding", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{
			Condition: dcgmapi.NvlinkPolicy,
			Data:      dcgmapi.NvlinkPolicyCondition{FieldId: 1, Counter: 5},
		}

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, "NvidiaNVLinkError", result.Reason)
	})

	t.Run("PageRetirementFinding", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{
			Condition: dcgmapi.MaxRtPgPolicy,
			Data:      dcgmapi.RetiredPagesPolicyCondition{SbePages: 1, DbePages: 2},
		}

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, "NvidiaPageRetirement", result.Reason)
	})

	t.Run("UnsupportedFinding", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{Condition: "Noop"}

		// wait so that it's more likely we run the code path
		time.Sleep(500 * time.Millisecond)

		select {
		case monitorResult := <-mgr.results:
			// There shouldn't have been any notification as this isn't a finding we subscribe to.
			t.Fatal("not expecting result", monitorResult)
		default:
			t.Log("No notification, as expected.")
		}
	})

	t.Run("DCGMError", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.ReconcileErr = fmt.Errorf("reconcileErr")
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, "DCGMError", result.Reason)
	})

	t.Run("DCGMDiagnosticFailure", func(t *testing.T) {
		t.Setenv("DCGM_DIAG_LEVEL", "1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.DiagResults = dcgmapi.DiagResults{
			Software: []dcgmapi.DiagResult{{Status: "fail"}},
		}
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, "DCGMDiagnosticFailure", result.Reason)
	})

	t.Run("NvidiaNCCLError", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, _ := newMonitorWithDcgm()
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		mgr.obs.Broadcast("nvidia", "segfault at XXXX in libnccl.so")

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, "NvidiaNCCLError", result.Reason)
	})

	t.Run("DCGMHealthCode109-WARN", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.HealthResponse = dcgmapi.HealthResponse{
			Incidents: []dcgmapi.Incident{
				{
					Health: dcgmapi.DCGM_HEALTH_RESULT_WARN,
					Error: dcgmapi.DiagErrorDetail{
						Code:    dcgmapi.DCGM_FR_SXID_ERROR,
						Message: "mock error",
					},
				},
			},
		}
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, "DCGMHealthCode109", result.Reason)
	})

	t.Run("DCGMHealthCode109-FAIL", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.HealthResponse = dcgmapi.HealthResponse{
			Incidents: []dcgmapi.Incident{
				{
					Health: dcgmapi.DCGM_HEALTH_RESULT_FAIL,
					Error: dcgmapi.DiagErrorDetail{
						Code:    dcgmapi.DCGM_FR_SXID_ERROR,
						Message: "mock error",
					},
				},
			},
		}
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityFatal, result.Severity)
		assert.Equal(t, "DCGMHealthCode109", result.Reason)
	})

	t.Run("DCGMFieldError112", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.FieldValues = []dcgmapi.FieldValue_v2{
			{
				FieldID: dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
				Status:  dcgmapi.DCGM_ST_BADPARAM,
			},
		}
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, "DCGMFieldError112", result.Reason)
	})

	t.Run("NvidiaDeviceCountMismatch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nvidiaMonitor, mockDcgm := newMonitorWithDcgm()
		mockDcgm.DeviceCount = 8
		mgr := newMockManager()
		nvidiaMonitor.Register(ctx, mgr)

		result := awaitCondition(t, mgr, testTimeout)
		assert.Equal(t, monitor.SeverityWarning, result.Severity)
		assert.Equal(t, "NvidiaDeviceCountMismatch", result.Reason)
	})
}
