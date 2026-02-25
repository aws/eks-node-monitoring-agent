package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const DefaultConfigPath = "/etc/nma/config.yaml"

// MonitorSettings holds per-monitor configuration.
type MonitorSettings struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
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

// KnownPluginNames is the set of valid plugin names for validation.
var KnownPluginNames = map[string]bool{
	"kernel-monitor":  true,
	"networking":      true,
	"storage-monitor": true,
	"nvidia":          true,
	"neuron":          true,
	"runtime":         true,
}

// Validate checks that all keys in Monitors are known plugin names.
func (mc *MonitorConfig) Validate() error {
	if mc == nil || mc.Monitors == nil {
		return nil
	}
	var unknown []string
	for name := range mc.Monitors {
		if !KnownPluginNames[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown monitor plugin name(s): %s", strings.Join(unknown, ", "))
	}
	return nil
}

// LoadMonitorConfig reads the config file at the given path.
// Returns a default (all-enabled) config if the file does not exist.
// Returns an error if the file exists but contains invalid YAML or unknown plugin names.
func LoadMonitorConfig(path string) (*MonitorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MonitorConfig{}, nil
		}
		return nil, fmt.Errorf("reading monitor config: %w", err)
	}

	// Empty file is treated as default config (all monitors enabled).
	if len(data) == 0 {
		return &MonitorConfig{}, nil
	}

	cfg := &MonitorConfig{}
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing monitor config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating monitor config: %w", err)
	}

	return cfg, nil
}
