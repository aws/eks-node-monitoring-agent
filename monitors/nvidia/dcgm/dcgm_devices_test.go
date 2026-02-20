//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestDeviceCount(t *testing.T) {
	t.Run("DeviceCountError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DeviceCountErr: fmt.Errorf("error")}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.ErrorIs(t, err, mockDcgm.DeviceCountErr)
		assert.Empty(t, conditions)
	})

	t.Run("IgnoreNotInitialized", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DeviceCountErr: dcgm.ErrNotInitialized}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("GetDeviceCounts", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DeviceCount: 8}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.NoError(t, err)
		assert.NotEmpty(t, conditions)
		assert.Equal(t, conditions[0], monitor.Condition{
			Reason:   "NvidiaDeviceCountMismatch",
			Message:  fmt.Sprintf("DCGM detected %d GPUs but %d nvidia device files were detected", 8, 0 /* test is not run on GPU */),
			Severity: monitor.SeverityWarning,
		})
	})
}
