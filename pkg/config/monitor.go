package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const DefaultConfigPath = "/etc/nma/config.yaml"

// MonitorSettings holds per-monitor configuration.
type MonitorSettings struct {
	Enabled                      *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AllowedIPTablesChains        []string `yaml:"allowedIPTablesChains,omitempty" json:"allowedIPTablesChains,omitempty"`
	ExcludedInterfaceNameRegexps []string `yaml:"excludedInterfaceNameRegexps,omitempty" json:"excludedInterfaceNameRegexps,omitempty"`
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

// DefaultExcludedInterfaceNameRegexps are the interface-name exclusion regexps
// applied when the networking monitor has none explicitly configured. It
// excludes Mellanox/NVIDIA IPoIB interfaces (e.g. "ibp115s0f0") by default,
// which are not node-networking interfaces and would otherwise cause false
// positive InterfaceNotUp / InterfaceNotRunning notifications.
var DefaultExcludedInterfaceNameRegexps = []string{`^ibp[0-9]+s[0-9]+f[0-9]+$`}

// GetExcludedInterfaceNameRegexps returns the interface-name exclusion regexps
// configured for the networking monitor. When the networking monitor has no
// excludedInterfaceNameRegexps explicitly set, DefaultExcludedInterfaceNameRegexps
// is returned. An explicitly configured empty list disables the default.
func (mc *MonitorConfig) GetExcludedInterfaceNameRegexps() []string {
	if mc == nil || mc.Monitors == nil {
		return DefaultExcludedInterfaceNameRegexps
	}
	settings, exists := mc.Monitors["networking"]
	if !exists || settings.ExcludedInterfaceNameRegexps == nil {
		return DefaultExcludedInterfaceNameRegexps
	}
	return settings.ExcludedInterfaceNameRegexps
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
		if len(settings.ExcludedInterfaceNameRegexps) > 0 {
			if name != "networking" {
				return fmt.Errorf("excludedInterfaceNameRegexps is only supported by the networking monitor, not %q", name)
			}
			for _, expr := range settings.ExcludedInterfaceNameRegexps {
				if strings.TrimSpace(expr) == "" {
					return fmt.Errorf("excludedInterfaceNameRegexps entry must not be empty or whitespace-only")
				}
				if _, err := regexp.Compile(expr); err != nil {
					return fmt.Errorf("excludedInterfaceNameRegexps entry %q is not a valid regular expression: %w", expr, err)
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
