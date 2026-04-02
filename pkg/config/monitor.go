package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const DefaultConfigPath = "/etc/nma/config.yaml"

// MonitorSettings holds per-monitor configuration.
type MonitorSettings struct {
	Enabled                      *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AllowedIPTablesChainPrefixes []string `yaml:"allowedIPTablesChainPrefixes,omitempty" json:"allowedIPTablesChainPrefixes,omitempty"`
}

// IsEnabled returns true if the monitor is enabled.
func (ms MonitorSettings) IsEnabled() bool {
	// Defaults to true when Enabled is nil (not explicitly set).
	// this is to ensure backward compatibility and consistency
	if ms.Enabled == nil {
		return true
	}
	return *ms.Enabled
}

// MonitorConfig is the top-level configuration structure.
type MonitorConfig struct {
	Monitors map[string]MonitorSettings `yaml:"monitors,omitempty" json:"monitors,omitempty"`
}

// IsMonitorEnabled checks if a given plugin is enabled.
// Returns true if the config is nil, the map is nil, or the plugin is not present in the map.
func (mc *MonitorConfig) IsMonitorEnabled(pluginName string) bool {
	if mc == nil || mc.Monitors == nil {
		return true
	}
	settings, exists := mc.Monitors[pluginName]
	if !exists {
		return true
	}
	return settings.IsEnabled()
}

// GetAllowedIPTablesChainPrefixes returns the allowed iptables chain prefixes
// configured for the networking monitor.
func (mc *MonitorConfig) GetAllowedIPTablesChainPrefixes() []string {
	if mc == nil || mc.Monitors == nil {
		return nil
	}
	settings, exists := mc.Monitors["networking"]
	if !exists {
		return nil
	}
	return settings.AllowedIPTablesChainPrefixes
}

// KnownPluginNames is the set of valid plugin names for validation.
var KnownPluginNames = []string{
	"kernel-monitor",
	"networking",
	"storage-monitor",
	"nvidia",
	"neuron",
	"runtime",
}

// Validate checks that all keys in Monitors are known plugin names.
func (mc *MonitorConfig) Validate() error {
	if mc == nil || mc.Monitors == nil {
		return nil
	}
	var unknown []string
	for name := range mc.Monitors {
		if !slices.Contains(KnownPluginNames, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown monitor plugin name(s): %s", strings.Join(unknown, ", "))
	}
	for name, settings := range mc.Monitors {
		if len(settings.AllowedIPTablesChainPrefixes) > 0 {
			if name != "networking" {
				return fmt.Errorf("allowedIPTablesChainPrefixes is only supported by the networking monitor, not %q", name)
			}
			for _, prefix := range settings.AllowedIPTablesChainPrefixes {
				if prefix == "" {
					return fmt.Errorf("allowedIPTablesChainPrefixes must not contain empty strings")
				}
			}
		}
	}
	return nil
}

// LoadMonitorConfig reads the config file at the given path.
// Returns a default (all-enabled) config if the file does not exist.
// Returns an error if the file exists but contains invalid YAML or unknown plugin names.
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
