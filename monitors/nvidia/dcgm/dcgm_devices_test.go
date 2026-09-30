//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func createFakeDevices(t *testing.T, dir string, count int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("nvidia%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func TestGetNvidiaFSDeviceCount(t *testing.T) {
	t.Run("StandardPath", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		createFakeDevices(t, filepath.Join(root, "dev"), 4)

		count, devDir, err := dcgm.GetNvidiaFSDeviceCount()
		assert.NoError(t, err)
		assert.Equal(t, uint(4), count)
		assert.Equal(t, filepath.Join(root, "dev"), devDir)
	})

	t.Run("GPUOperatorAutoDetect", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		// No devices in standard /dev, but GPU Operator path has them
		gpuOpDev := filepath.Join(root, "run", "nvidia", "driver", "dev")
		createFakeDevices(t, gpuOpDev, 8)

		count, devDir, err := dcgm.GetNvidiaFSDeviceCount()
		assert.NoError(t, err)
		assert.Equal(t, uint(8), count)
		assert.Equal(t, gpuOpDev, devDir)
	})

	t.Run("ExplicitOverride", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "/custom/driver")
		customDev := filepath.Join(root, "custom", "driver", "dev")
		createFakeDevices(t, customDev, 2)

		count, devDir, err := dcgm.GetNvidiaFSDeviceCount()
		assert.NoError(t, err)
		assert.Equal(t, uint(2), count)
		assert.Equal(t, customDev, devDir)
	})

	t.Run("EmptyGPUOperatorDirFallsBackToStandardDev", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		// GPU Operator dir exists but is empty; standard /dev has devices
		os.MkdirAll(filepath.Join(root, "run", "nvidia", "driver", "dev"), 0o755)
		createFakeDevices(t, filepath.Join(root, "dev"), 4)

		count, devDir, err := dcgm.GetNvidiaFSDeviceCount()
		assert.NoError(t, err)
		assert.Equal(t, uint(4), count)
		assert.Equal(t, filepath.Join(root, "dev"), devDir)
	})

	t.Run("NoDevicesAnywhere", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		os.MkdirAll(filepath.Join(root, "dev"), 0o755)

		count, _, err := dcgm.GetNvidiaFSDeviceCount()
		assert.NoError(t, err)
		assert.Equal(t, uint(0), count)
	})
}

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
		assert.Equal(t, "NvidiaDeviceCountMismatch", conditions[0].Reason)
		assert.Equal(t, monitor.SeverityFatal, conditions[0].Severity)
		assert.Contains(t, conditions[0].Message, "DCGM detected 8 GPUs but 0 nvidia device files were detected at")
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
