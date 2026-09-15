package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func TestHostRoot(t *testing.T) {
	t.Run("WithoutRoot", func(t *testing.T) {
		assert.Equal(t, "/", config.HostRoot())
	})

	t.Run("WithRoot", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, tempRoot)
		assert.Equal(t, tempRoot, config.HostRoot())
	})

	t.Run("Path", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, tempRoot)
		assert.Equal(t, filepath.Join(config.HostRoot(), "cowsay"), config.ToHostPath("cowsay"))
	})
}

func TestNvidiaDriverRoot(t *testing.T) {
	t.Run("UnsetReturnsEmpty", func(t *testing.T) {
		assert.Equal(t, "", config.NvidiaDriverRoot())
	})

	t.Run("ReturnsEnvValue", func(t *testing.T) {
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "/run/nvidia/driver")
		assert.Equal(t, "/run/nvidia/driver", config.NvidiaDriverRoot())
	})
}
