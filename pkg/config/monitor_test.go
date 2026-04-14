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
	cfg, found, err := config.LoadMonitorConfig("/tmp/does-not-exist-nma-test.yaml")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.False(t, found, "expected found to be false for non-existent file")
	// Default config: all monitors enabled (zero-value MonitorsConfig).
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

	cfg, found, err := config.LoadMonitorConfig(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, found)

	// kernel-monitor should be explicitly disabled.
	assert.False(t, cfg.IsMonitorEnabled("kernel-monitor"))
	// Other monitors should remain enabled (absent from config → default true).
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

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
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

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	// Unknown monitor names are now caught at parse time by strict YAML unmarshaling.
	assert.Contains(t, err.Error(), "unknown-plugin")
	assert.Contains(t, err.Error(), "parsing monitor config")
}

func TestLoadMonitorConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	require.NoError(t, os.WriteFile(cfgPath, []byte(""), 0644))

	cfg, found, err := config.LoadMonitorConfig(cfgPath)
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.True(t, found)
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

func TestIsMonitorEnabled_ZeroValueConfig(t *testing.T) {
	cfg := &config.MonitorConfig{}
	assert.True(t, cfg.IsMonitorEnabled("kernel-monitor"))
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_AbsentPlugin(t *testing.T) {
	// networking is explicitly disabled; kernel-monitor is absent (zero value) → enabled.
	cfg := &config.MonitorConfig{
		Monitors: config.MonitorsConfig{
			Networking: config.NetworkingMonitorSettings{
				MonitorSettings: config.MonitorSettings{Enabled: boolPtr(false)},
			},
		},
	}
	assert.True(t, cfg.IsMonitorEnabled("kernel-monitor"))
}

func TestIsMonitorEnabled_ExplicitlyEnabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: config.MonitorsConfig{
			Networking: config.NetworkingMonitorSettings{
				MonitorSettings: config.MonitorSettings{Enabled: boolPtr(true)},
			},
		},
	}
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_ExplicitlyDisabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: config.MonitorsConfig{
			Networking: config.NetworkingMonitorSettings{
				MonitorSettings: config.MonitorSettings{Enabled: boolPtr(false)},
			},
		},
	}
	assert.False(t, cfg.IsMonitorEnabled("networking"))
}

func TestIsMonitorEnabled_NilEnabled(t *testing.T) {
	cfg := &config.MonitorConfig{
		Monitors: config.MonitorsConfig{
			Networking: config.NetworkingMonitorSettings{
				MonitorSettings: config.MonitorSettings{Enabled: nil},
			},
		},
	}
	// nil Enabled → defaults to true.
	assert.True(t, cfg.IsMonitorEnabled("networking"))
}

func TestGetNetworkingSettings(t *testing.T) {
	t.Run("NilConfig", func(t *testing.T) {
		var cfg *config.MonitorConfig
		assert.Empty(t, cfg.GetNetworkingSettings().AllowedIPTablesChains)
	})
	t.Run("ZeroValueConfig", func(t *testing.T) {
		cfg := &config.MonitorConfig{}
		assert.Empty(t, cfg.GetNetworkingSettings().AllowedIPTablesChains)
	})
	t.Run("NoNetworkingEntry", func(t *testing.T) {
		cfg := &config.MonitorConfig{
			Monitors: config.MonitorsConfig{
				Kernel: config.MonitorSettings{Enabled: boolPtr(true)},
			},
		}
		assert.Empty(t, cfg.GetNetworkingSettings().AllowedIPTablesChains)
	})
	t.Run("WithChains", func(t *testing.T) {
		cfg := &config.MonitorConfig{
			Monitors: config.MonitorsConfig{
				Networking: config.NetworkingMonitorSettings{
					AllowedIPTablesChains: []string{"filter/MY-CUSTOM-CHAIN", "filter/CUSTOM-CHAIN"},
				},
			},
		}
		assert.Equal(t, []string{"filter/MY-CUSTOM-CHAIN", "filter/CUSTOM-CHAIN"}, cfg.GetNetworkingSettings().AllowedIPTablesChains)
	})
}

func TestLoadMonitorConfig_AllowedIPTablesChains(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    enabled: true
    allowedIPTablesChains:
      - "filter/MY-CUSTOM-CHAIN"
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, found, err := config.LoadMonitorConfig(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, found)
	assert.True(t, cfg.IsMonitorEnabled("networking"))
	assert.Equal(t, []string{"filter/MY-CUSTOM-CHAIN"}, cfg.GetNetworkingSettings().AllowedIPTablesChains)
}

func TestLoadMonitorConfig_EmptyChainRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    allowedIPTablesChains:
      - ""
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "must use \"table/chain\" format")
}

func TestLoadMonitorConfig_WhitespaceOnlyChainRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    allowedIPTablesChains:
      - "   "
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "must not have leading or trailing whitespace")
}

func TestLoadMonitorConfig_UnqualifiedChainRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    allowedIPTablesChains:
      - "MY-CUSTOM-CHAIN"
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "must use \"table/chain\" format")
}

func TestLoadMonitorConfig_ChainWithExtraSlashRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    allowedIPTablesChains:
      - "filter/MY/CUSTOM-CHAIN"
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "must use \"table/chain\" format")
}

func TestLoadMonitorConfig_ChainWithSurroundingWhitespaceRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  networking:
    allowedIPTablesChains:
      - " filter/MY-CUSTOM-CHAIN "
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "must not have leading or trailing whitespace")
}

func TestLoadMonitorConfig_AllowedIPTablesChainsOnNonNetworkingMonitorRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := []byte(`monitors:
  kernel-monitor:
    allowedIPTablesChains:
      - "filter/MY-CUSTOM-CHAIN"
`)
	require.NoError(t, os.WriteFile(cfgPath, content, 0644))

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	// allowedIPTablesChains is not a field of MonitorSettings (the type for kernel-monitor),
	// so it is now rejected at parse time by strict YAML unmarshaling.
	assert.Contains(t, err.Error(), "allowedIPTablesChains")
	assert.Contains(t, err.Error(), "parsing monitor config")
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

	cfg, _, err := config.LoadMonitorConfig(cfgPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "parsing monitor config")
}
