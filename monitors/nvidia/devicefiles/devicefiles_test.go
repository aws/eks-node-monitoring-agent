package devicefiles_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/devicefiles"
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

func TestCount(t *testing.T) {
	t.Run("StandardPath", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		createFakeDevices(t, filepath.Join(root, "dev"), 4)

		count, devDir, err := devicefiles.Count()
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

		count, devDir, err := devicefiles.Count()
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

		count, devDir, err := devicefiles.Count()
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

		count, devDir, err := devicefiles.Count()
		assert.NoError(t, err)
		assert.Equal(t, uint(4), count)
		assert.Equal(t, filepath.Join(root, "dev"), devDir)
	})

	t.Run("NoDevicesAnywhere", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, root)
		os.MkdirAll(filepath.Join(root, "dev"), 0o755)

		count, _, err := devicefiles.Count()
		assert.NoError(t, err)
		assert.Equal(t, uint(0), count)
	})
}
