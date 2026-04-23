//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
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
			Severity: monitor.SeverityFatal,
		})
	})
}

func TestDeviceCountExpectedMismatch(t *testing.T) {
	t.Run("DetectsMissingGPU", func(t *testing.T) {
		// Simulate a g6e.12xlarge where one GPU fell off the PCIe bus:
		// DCGM sees 3, /dev has 3 (so existing check passes), but instance type expects 4.
		mockDcgm := &fake.FakeDcgm{DeviceCount: 3}
		fakeProvider := &fake.FakeInstanceTypeInfoProvider{
			Info: &instanceinfo.InstanceInfo{InstanceType: "g6e.12xlarge", NvidiaGPUCount: 4},
		}
		dcgmSystem := dcgm.NewDCGMSystemWithInstanceTypeInfoProvider(mockDcgm, dcgm.GetDiagType(), fakeProvider)
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.NoError(t, err)

		// Find the condition for expected count mismatch (same reason, distinguished by message)
		var expectedMismatch *monitor.Condition
		for i, c := range conditions {
			if c.Reason == "NvidiaDeviceCountMismatch" && strings.Contains(c.Message, "expected") {
				expectedMismatch = &conditions[i]
				break
			}
		}
		assert.NotNil(t, expectedMismatch, "expected NvidiaDeviceCountMismatch condition for instance type mismatch")
		assert.Equal(t, monitor.SeverityFatal, expectedMismatch.Severity)
		assert.Contains(t, expectedMismatch.Message, "expected 4 GPUs")
		assert.Contains(t, expectedMismatch.Message, "only 3 were detected")
	})

	t.Run("NoConditionWhenCountMatches", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DeviceCount: 4}
		fakeProvider := &fake.FakeInstanceTypeInfoProvider{
			Info: &instanceinfo.InstanceInfo{InstanceType: "g6e.12xlarge", NvidiaGPUCount: 4},
		}
		dcgmSystem := dcgm.NewDCGMSystemWithInstanceTypeInfoProvider(mockDcgm, dcgm.GetDiagType(), fakeProvider)
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.NoError(t, err)

		for _, c := range conditions {
			if c.Reason == "NvidiaDeviceCountMismatch" {
				assert.NotContains(t, c.Message, "expected",
					"should not report instance type mismatch when GPU count matches")
			}
		}
	})

	t.Run("HandlesProviderError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{DeviceCount: 3}
		fakeProvider := &fake.FakeInstanceTypeInfoProvider{Err: fmt.Errorf("IMDS unavailable")}
		dcgmSystem := dcgm.NewDCGMSystemWithInstanceTypeInfoProvider(mockDcgm, dcgm.GetDiagType(), fakeProvider)
		conditions, err := dcgmSystem.DeviceCount(context.TODO())
		assert.NoError(t, err)

		for _, c := range conditions {
			if c.Reason == "NvidiaDeviceCountMismatch" {
				assert.NotContains(t, c.Message, "expected",
					"should not report instance type mismatch when provider fails")
			}
		}
	})
}
