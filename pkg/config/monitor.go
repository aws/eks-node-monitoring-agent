package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

const DefaultConfigPath = "/etc/nma/config.yaml"

// MonitorSettings holds settings common to all monitors.
type MonitorSettings struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IsEnabled returns true if the monitor is enabled.
// Defaults to true when Enabled is nil.
func (ms MonitorSettings) IsEnabled() bool {
	if ms.Enabled == nil {
		return true
	}
	return *ms.Enabled
}

// NetworkingMonitorSettings extends MonitorSettings with networking-specific fields.
type NetworkingMonitorSettings struct {
	MonitorSettings
	AllowedIPTablesChains []string `yaml:"allowedIPTablesChains,omitempty" json:"allowedIPTablesChains,omitempty"`
}

// MonitorsConfig holds per-monitor configuration, with a dedicated field per monitor.
type MonitorsConfig struct {
	Kernel     MonitorSettings           `yaml:"kernel-monitor,omitempty" json:"kernel-monitor,omitempty"`
	Networking NetworkingMonitorSettings `yaml:"networking,omitempty" json:"networking,omitempty"`
	Storage    MonitorSettings           `yaml:"storage-monitor,omitempty" json:"storage-monitor,omitempty"`
	Nvidia     MonitorSettings           `yaml:"nvidia,omitempty" json:"nvidia,omitempty"`
	Neuron     MonitorSettings           `yaml:"neuron,omitempty" json:"neuron,omitempty"`
	Runtime    MonitorSettings           `yaml:"runtime,omitempty" json:"runtime,omitempty"`
}

// MonitorConfig is the top-level configuration structure.
type MonitorConfig struct {
	Monitors MonitorsConfig `yaml:"monitors,omitempty" json:"monitors,omitempty"`
}

// KnownPluginNames is the set of valid plugin names.
var KnownPluginNames = []string{
	"kernel-monitor",
	"networking",
	"storage-monitor",
	"nvidia",
	"neuron",
	"runtime",
}

// IsMonitorEnabled checks if a given plugin is enabled.
// Returns true if the config is nil or the plugin name is unknown.
func (mc *MonitorConfig) IsMonitorEnabled(pluginName string) bool {
	if mc == nil {
		return true
	}
	switch pluginName {
	case "kernel-monitor":
		return mc.Monitors.Kernel.IsEnabled()
	case "networking":
		return mc.Monitors.Networking.IsEnabled()
	case "storage-monitor":
		return mc.Monitors.Storage.IsEnabled()
	case "nvidia":
		return mc.Monitors.Nvidia.IsEnabled()
	case "neuron":
		return mc.Monitors.Neuron.IsEnabled()
	case "runtime":
		return mc.Monitors.Runtime.IsEnabled()
	default:
		return true
	}
}

// GetNetworkingSettings returns the settings for the networking monitor.
func (mc *MonitorConfig) GetNetworkingSettings() NetworkingMonitorSettings {
	if mc == nil {
		return NetworkingMonitorSettings{}
	}
	return mc.Monitors.Networking
}

// Validate checks the semantic validity of settings not covered by strict YAML parsing.
func (mc *MonitorConfig) Validate() error {
	if mc == nil {
		return nil
	}
	for _, chain := range mc.Monitors.Networking.AllowedIPTablesChains {
		if strings.TrimSpace(chain) != chain {
			return fmt.Errorf("allowedIPTablesChains entry %q must not have leading or trailing whitespace", chain)
		}
		table, chainName, ok := strings.Cut(chain, "/")
		if !ok || strings.Count(chain, "/") != 1 || strings.TrimSpace(table) == "" || strings.TrimSpace(chainName) == "" {
			return fmt.Errorf("allowedIPTablesChains entry %q must use \"table/chain\" format with non-empty table and chain (e.g. \"filter/MY-CUSTOM-CHAIN\")", chain)
		}
	}
	return nil
}

// LoadMonitorConfig reads the config file at the given path.
// Returns a default (all-enabled) config if the file does not exist.
// Returns an error if the file exists but contains invalid YAML or unknown monitor names.
// The second return value indicates whether the config file was found on disk.
func LoadMonitorConfig(path string) (*MonitorConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MonitorConfig{}, false, nil
		}
		return nil, false, fmt.Errorf("reading monitor config: %w", err)
	}

	// Empty file is treated as default config (all monitors enabled).
	if len(data) == 0 {
		return &MonitorConfig{}, true, nil
	}

	cfg := &MonitorConfig{}
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return nil, false, fmt.Errorf("parsing monitor config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, false, fmt.Errorf("validating monitor config: %w", err)
	}

	return cfg, true, nil
}
