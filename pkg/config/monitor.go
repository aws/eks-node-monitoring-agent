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
	Enabled               *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AllowedIPTablesChains []string `yaml:"allowedIPTablesChains,omitempty" json:"allowedIPTablesChains,omitempty"`
}

// EventConfig holds agent-level event configuration.
type EventConfig struct {
	DisabledEvents  []string       `yaml:"disabledEvents,omitempty" json:"disabledEvents,omitempty"`
	EventThresholds map[string]int `yaml:"eventThresholds,omitempty" json:"eventThresholds,omitempty"`
}

// IsEventDisabled returns true when an event has been disabled by name.
func (ec EventConfig) IsEventDisabled(eventName string) bool {
	return slices.Contains(ec.DisabledEvents, eventName)
}

// EventThreshold returns a configured event threshold, or the provided default.
func (ec EventConfig) EventThreshold(eventName string, defaultThreshold int) int {
	if ec.EventThresholds == nil {
		return defaultThreshold
	}
	threshold, exists := ec.EventThresholds[eventName]
	if !exists {
		return defaultThreshold
	}
	return threshold
}

// IsZero returns true when no event configuration is set.
func (ec EventConfig) IsZero() bool {
	return len(ec.DisabledEvents) == 0 && len(ec.EventThresholds) == 0
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
	Monitors        map[string]MonitorSettings `yaml:"monitors,omitempty" json:"monitors,omitempty"`
	DisabledEvents  []string                   `yaml:"disabledEvents,omitempty" json:"disabledEvents,omitempty"`
	EventThresholds map[string]int             `yaml:"eventThresholds,omitempty" json:"eventThresholds,omitempty"`
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

// GetAllowedIPTablesChains returns the allowed iptables chains
// configured for the networking monitor.
func (mc *MonitorConfig) GetAllowedIPTablesChains() []string {
	if mc == nil || mc.Monitors == nil {
		return nil
	}
	settings, exists := mc.Monitors["networking"]
	if !exists {
		return nil
	}
	return settings.AllowedIPTablesChains
}

// GetEventConfig returns the agent-level event configuration.
func (mc *MonitorConfig) GetEventConfig() EventConfig {
	if mc == nil {
		return EventConfig{}
	}
	return EventConfig{
		DisabledEvents:  mc.DisabledEvents,
		EventThresholds: mc.EventThresholds,
	}
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

// KnownEventNames is the set of valid agent event names for validation.
var KnownEventNames = []string{
	"LargeEnvironment",
}

// Validate checks that all keys in Monitors are known plugin names.
func (mc *MonitorConfig) Validate() error {
	if mc == nil {
		return nil
	}
	if mc.Monitors == nil {
		return mc.GetEventConfig().Validate()
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
		if len(settings.AllowedIPTablesChains) > 0 {
			if name != "networking" {
				return fmt.Errorf("allowedIPTablesChains is only supported by the networking monitor, not %q", name)
			}
			for _, chain := range settings.AllowedIPTablesChains {
				if strings.TrimSpace(chain) != chain {
					return fmt.Errorf("allowedIPTablesChains entry %q must not have leading or trailing whitespace", chain)
				}
				table, chainName, ok := strings.Cut(chain, "/")
				if !ok || strings.Count(chain, "/") != 1 || strings.TrimSpace(table) == "" || strings.TrimSpace(chainName) == "" {
					return fmt.Errorf("allowedIPTablesChains entry %q must use \"table/chain\" format with non-empty table and chain (e.g. \"filter/MY-CUSTOM-CHAIN\")", chain)
				}
			}
		}
	}
	if err := mc.GetEventConfig().Validate(); err != nil {
		return err
	}
	return nil
}

// Validate checks that event configuration values are well-formed.
func (ec EventConfig) Validate() error {
	for _, eventName := range ec.DisabledEvents {
		if err := validateEventName("disabledEvents", eventName); err != nil {
			return err
		}
	}
	for eventName, threshold := range ec.EventThresholds {
		if err := validateEventName("eventThresholds", eventName); err != nil {
			return err
		}
		if threshold <= 0 {
			return fmt.Errorf("eventThresholds entry %q must be positive", eventName)
		}
	}
	return nil
}

func validateEventName(fieldName, eventName string) error {
	if strings.TrimSpace(eventName) != eventName {
		return fmt.Errorf("%s event name %q must not have leading or trailing whitespace", fieldName, eventName)
	}
	if eventName == "" {
		return fmt.Errorf("%s event name must not be empty", fieldName)
	}
	if !slices.Contains(KnownEventNames, eventName) {
		return fmt.Errorf("%s event name %q is not supported", fieldName, eventName)
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
