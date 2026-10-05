//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestHealthCheck(t *testing.T) {
	t.Run("HealthCheckError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{HealthErr: fmt.Errorf("error")}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.HealthCheck(context.TODO())
		assert.ErrorIs(t, err, mockDcgm.HealthErr)
		assert.Empty(t, conditions)
	})

	t.Run("IgnoreNotInitialized", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{HealthErr: dcgm.ErrNotInitialized}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.HealthCheck(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("GetResult", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{
			HealthResponse: dcgmapi.HealthResponse{
				Incidents: []dcgmapi.Incident{
					{
						Health: dcgmapi.DCGM_HEALTH_RESULT_FAIL,
						Error: dcgmapi.DiagErrorDetail{
							Code:    dcgmapi.DCGM_FR_SXID_ERROR,
							Message: "mock error",
						},
					},
				},
			},
		}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.HealthCheck(context.TODO())
		assert.NoError(t, err)
		assert.NotEmpty(t, conditions)
		assert.Equal(t, conditions[0], monitor.Condition{
			Reason:   fmt.Sprintf("DCGMHealthCode%d", mockDcgm.HealthResponse.Incidents[0].Error.Code),
			Message:  fmt.Sprintf("DCGM detected issues in health check system with error code %d", mockDcgm.HealthResponse.Incidents[0].Error.Code),
			Severity: monitor.SeverityFatal,
		})
	})

	// DCGM_FR_NVLINK_ERROR_CRITICAL (71) from the lifetime-absolute counter path
	// must be downgraded to Warning on pre-Blackwell GPUs. The counter never
	// resets without a GPU reset, so a non-zero value is not an active-fault
	// signal. The message prefix is stable because DCGM_VERSION is pinned.
	t.Run("NVLinkLifetimeCounterDowngradedToWarning", func(t *testing.T) {
		for _, msg := range []string{
			"Detected 1 datalink layer CRC error counter NvLink errors on GPU 7's NVLink (should be 0)",
			"Detected 1 datalink layer recovery error counter NvLink errors on GPU 0's NVLink (should be 0)",
			"Detected 1 datalink layer replay error counter NvLink errors on GPU 3's NVLink (should be 0)",
		} {
			mockDcgm := &fake.FakeDcgm{
				HealthResponse: dcgmapi.HealthResponse{
					Incidents: []dcgmapi.Incident{
						{
							System: dcgmapi.DCGM_HEALTH_WATCH_NVLINK,
							Health: dcgmapi.DCGM_HEALTH_RESULT_FAIL,
							Error: dcgmapi.DiagErrorDetail{
								Code:    dcgmapi.DCGM_FR_NVLINK_ERROR_CRITICAL,
								Message: msg,
							},
						},
					},
				},
			}
			dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
			conditions, err := dcgmSystem.HealthCheck(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1, "expected one condition for message: %s", msg)
			assert.Equal(t, monitor.SeverityWarning, conditions[0].Severity,
				"lifetime counter code 71 must be Warning, not Fatal (message: %s)", msg)
		}
	})

	// Code 71 from a different message (active-fault path) must stay Fatal.
	t.Run("NVLinkActiveFaultRemainsFatal", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{
			HealthResponse: dcgmapi.HealthResponse{
				Incidents: []dcgmapi.Incident{
					{
						System: dcgmapi.DCGM_HEALTH_WATCH_NVLINK,
						Health: dcgmapi.DCGM_HEALTH_RESULT_FAIL,
						Error: dcgmapi.DiagErrorDetail{
							Code:    dcgmapi.DCGM_FR_NVLINK_ERROR_CRITICAL,
							Message: "NVLink failure detected on link 3",
						},
					},
				},
			},
		}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.HealthCheck(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 1)
		assert.Equal(t, monitor.SeverityFatal, conditions[0].Severity,
			"code 71 without a lifetime-counter message must remain Fatal")
	})

	// DCGM_FR_FABRIC_PROBE_STATE (123) comes from DCGM's check of the Fabric
	// Manager status field, which WatchFields classifies itself, so it is a
	// Warning whatever DCGM's verdict. The NvmlTooOld and In Progress messages
	// were captured from DCGM 4.6.1 on a g5 (driver 535) and a p5 with Fabric
	// Manager not running; the others follow the same DCGM message format.
	t.Run("FabricProbeStateIsWarning", func(t *testing.T) {
		for _, tc := range []struct {
			health  dcgmapi.HealthResult
			message string
		}{
			{dcgmapi.DCGM_HEALTH_RESULT_FAIL, "GPU 0: Fabric State is NvmlTooOld (6). Ensure that the FabricManager is running without errors. NVML version is too old to query Fabric Manager status."},
			{dcgmapi.DCGM_HEALTH_RESULT_FAIL, "GPU 0: Fabric State is Not Started (1). Ensure that the FabricManager is running without errors. Fabric Manager training has not started."},
			{dcgmapi.DCGM_HEALTH_RESULT_FAIL, "GPU 0: Fabric State is Failed (4). Ensure that the FabricManager is running without errors. Fabric Manager training failed."},
			{dcgmapi.DCGM_HEALTH_RESULT_FAIL, "GPU 0: Fabric State is Unknown (-1). Ensure that the FabricManager is running without errors. Unknown Fabric Manager status code: -1"},
			{dcgmapi.DCGM_HEALTH_RESULT_WARN, "GPU 0: Fabric State is In Progress (2). Ensure that the FabricManager is running without errors. Fabric Manager training in progress."},
		} {
			mockDcgm := &fake.FakeDcgm{
				HealthResponse: dcgmapi.HealthResponse{
					Incidents: []dcgmapi.Incident{
						{
							System: dcgmapi.DCGM_HEALTH_WATCH_NVLINK,
							Health: tc.health,
							Error: dcgmapi.DiagErrorDetail{
								Code:    dcgmapi.DCGM_FR_FABRIC_PROBE_STATE,
								Message: tc.message,
							},
						},
					},
				},
			}
			dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
			conditions, err := dcgmSystem.HealthCheck(context.TODO())
			assert.NoError(t, err)
			assert.Equal(t, []monitor.Condition{{
				Reason:   "DCGMHealthCode123",
				Message:  "DCGM detected issues in health check system with error code 123",
				Severity: monitor.SeverityWarning,
			}}, conditions, "message: %s", tc.message)
		}
	})

	// DCGM reports the fabric probe state for every GPU on every call while the
	// Fabric Manager status is unchanged, so its Warning is rate-limited:
	// reported once, not again while unchanged until WarningReNotifyInterval
	// has passed, and right away again once it has cleared and comes back.
	// Other Warnings are not rate-limited.
	t.Run("FabricProbeStateWarningRateLimited", func(t *testing.T) {
		fabricProbeIncident := func(gpu string) dcgmapi.Incident {
			return dcgmapi.Incident{
				System: dcgmapi.DCGM_HEALTH_WATCH_NVLINK,
				Health: dcgmapi.DCGM_HEALTH_RESULT_FAIL,
				Error: dcgmapi.DiagErrorDetail{
					Code:    dcgmapi.DCGM_FR_FABRIC_PROBE_STATE,
					Message: "GPU " + gpu + ": Fabric State is NvmlTooOld (6). Ensure that the FabricManager is running without errors. NVML version is too old to query Fabric Manager status.",
				},
			}
		}
		powerIncident := dcgmapi.Incident{Health: dcgmapi.DCGM_HEALTH_RESULT_WARN, Error: dcgmapi.DiagErrorDetail{Code: dcgmapi.DCGM_FR_CLOCK_THROTTLE_POWER}}
		withFabricProbe := dcgmapi.HealthResponse{Incidents: []dcgmapi.Incident{fabricProbeIncident("0"), fabricProbeIncident("1"), powerIncident}}
		withoutFabricProbe := dcgmapi.HealthResponse{Incidents: []dcgmapi.Incident{powerIncident}}

		mockDcgm := &fake.FakeDcgm{HealthResponse: withFabricProbe}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		now := time.Unix(0, 0)
		dcgm.SetClock(dcgmSystem, func() time.Time { return now })
		fabricProbeState := monitor.Condition{Reason: "DCGMHealthCode123", Message: "DCGM detected issues in health check system with error code 123", Severity: monitor.SeverityWarning}
		power := monitor.Condition{
			Reason:   fmt.Sprintf("DCGMHealthCode%d", dcgmapi.DCGM_FR_CLOCK_THROTTLE_POWER),
			Message:  fmt.Sprintf("DCGM detected issues in health check system with error code %d", dcgmapi.DCGM_FR_CLOCK_THROTTLE_POWER),
			Severity: monitor.SeverityWarning,
		}
		check := func() []monitor.Condition {
			t.Helper()
			conditions, err := dcgmSystem.HealthCheck(context.TODO())
			assert.NoError(t, err)
			return conditions
		}

		// First call: the 123 Warning once (not once per GPU), and the power Warning.
		assert.Equal(t, []monitor.Condition{fabricProbeState, power}, check())

		// Next call, unchanged: only the power Warning.
		now = now.Add(5 * time.Minute)
		assert.Equal(t, []monitor.Condition{power}, check())

		// Still unchanged once WarningReNotifyInterval has passed: reported again.
		now = now.Add(dcgm.WarningReNotifyInterval - 5*time.Minute)
		assert.Equal(t, []monitor.Condition{fabricProbeState, power}, check())

		// Cleared, then back a minute later: reported right away.
		mockDcgm.HealthResponse = withoutFabricProbe
		now = now.Add(time.Minute)
		assert.Equal(t, []monitor.Condition{power}, check())
		mockDcgm.HealthResponse = withFabricProbe
		now = now.Add(time.Minute)
		assert.Equal(t, []monitor.Condition{fabricProbeState, power}, check())
	})
}
