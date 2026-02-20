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
}
