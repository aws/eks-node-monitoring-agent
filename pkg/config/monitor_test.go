package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestLoadMonitorConfig_NonExistentFile(t *testing.T) {
	cfg, err := config.LoadMonitorConfig("/tmp/does-not-exist-nma-test.yaml")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	// Default config: all monitors enabled (empty map).
	assert.Empty(t, cfg.Monitors)
	// Every known plugin should be enabled by default.
	for _, name := range config.KnownPluginNames {
		assert.True(t, cfg.IsMonitorEnabled(name), "expected %s to be enabled by default", name)
	}
}

func TestLoadMonitorConfig_ValidFileOneDisabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  kernel-monitor:
    enabled: false
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, err := config.LoadMonitorConfig(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// kernel-monitor should be explicitly disabled.
	assert.False(t, cfg.IsMonitorEnabled("kernel-monitor"))
	// Other monitors should remain enabled (absent from map → default true).
	assert.True(t, cfg.IsMonitorEnabled("networking"))
	assert.True(t, cfg.IsMonitorEnabled("storage-monitor"))
	assert.True(t, cfg.IsMonitorEnabled("nvidia"))
	assert.True(t, cfg.IsMonitorEnabled("neuron"))
	assert.True(t, cfg.IsMonitorEnabled("runtime"))
}

func TestLoadMonitorConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors: [this is not valid: yaml: {{{`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "parsing monitor config")
}

func TestLoadMonitorConfig_UnknownPluginName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  unknown-plugin:
    enabled: false
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "unknown-plugin")
	assert.Contains(t, err.Error(), "validating monitor config")
}

func TestLoadMonitorConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	require.NoError(t, os.WriteFile(cfgPath, []byte(""), 0644))

	cfg, err := config.LoadMonitorConfig(cfgPath)
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Empty(t, cfg.Monitors)
	// All monitors should be enabled by default.
	for _, name := range config.KnownPluginNames {
		assert.True(t, cfg.IsMonitorEnabled(name), "expected %s to be enabled for empty file", name)
	}
}

func TestIsMonitorEnabled_NilConfig(t *testing.T) {
	var cfg *config.MonitorConfig
	assert.True(t, cfg.IsMonitorEnabled("kernel-monitor"))
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_EmptyMap(t *testing.T) {
	cfg := &config.MonitorConfig{}
	assert.True(t, cfg.IsMonitorEnabled("kernel-monitor"))
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_AbsentPlugin(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: map[string]config.MonitorSettings{
			"networking": {Enabled: boolPtr(false)},
		},
	}
	// kernel-monitor is absent from the map → should be enabled.
	assert.True(t, cfg.IsMonitorEnabled("kernel-monitor"))
}

func TestIsMonitorEnabled_ExplicitlyEnabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: map[string]config.MonitorSettings{
			"networking": {Enabled: boolPtr(true)},
		},
	}
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_ExplicitlyDisabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: map[string]config.MonitorSettings{
			"networking": {Enabled: boolPtr(false)},
		},
	}
	assert.False(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_NilEnabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: map[string]config.MonitorSettings{
			"networking": {Enabled: nil},
		},
	}
	// nil Enabled → defaults to true.
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestLoadMonitorConfig_StrictUnmarshalRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  kernel-monitor:
    enabled: true
    unknownField: 42
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "parsing monitor config")
}
