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

func TestActiveDiagnostic(t *testing.T) {
	t.Run("NoDiagType", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.ActiveDiagnostic(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("BadDiagType", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{}
		// the system will not parse the value. and will continue to use 0
		t.Setenv("DCGM_DIAG_LEVEL", "foo")
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.ActiveDiagnostic(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("DiagError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DiagErr: fmt.Errorf("error")}
		t.Setenv("DCGM_DIAG_LEVEL", "1")
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.ActiveDiagnostic(context.TODO())
		assert.ErrorIs(t, err, mockDcgm.DiagErr)
		assert.Empty(t, conditions)
	})

	t.Run("IgnoreNotInitialized", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DiagErr: dcgm.ErrNotInitialized}
		t.Setenv("DCGM_DIAG_LEVEL", "1")
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.ActiveDiagnostic(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("GetResult", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{
			DiagResults: dcgmapi.DiagResults{
				Software: []dcgmapi.DiagResult{
					{
						Status:       "fail",
						TestName:     "mock",
						ErrorMessage: "mock",
					},
				},
			},
		}
		t.Setenv("DCGM_DIAG_LEVEL", "1")
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.ActiveDiagnostic(context.TODO())
		assert.NoError(t, err)
		assert.NotEmpty(t, conditions)
		assert.Equal(t, conditions[0], monitor.Condition{
			Reason:   "DCGMDiagnosticFailure",
			Message:  "DCGM Diagnostic failed for test mock with error: mock",
			Severity: monitor.SeverityFatal,
		})
	})
}
