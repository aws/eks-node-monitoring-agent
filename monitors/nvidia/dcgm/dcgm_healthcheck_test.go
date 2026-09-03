//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"testing"

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
}
