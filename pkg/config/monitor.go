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

// KernelMonitorSettings holds settings for the kernel monitor.
type KernelMonitorSettings struct {
	MonitorSettings
}

// NetworkingMonitorSettings holds settings for the networking monitor.
type NetworkingMonitorSettings struct {
	MonitorSettings
	AllowedIPTablesChains []string `yaml:"allowedIPTablesChains,omitempty" json:"allowedIPTablesChains,omitempty"`
}

// StorageMonitorSettings holds settings for the storage monitor.
type StorageMonitorSettings struct {
	MonitorSettings
}

// NvidiaMonitorSettings holds settings for the nvidia monitor.
type NvidiaMonitorSettings struct {
	MonitorSettings
}

// NeuronMonitorSettings holds settings for the neuron monitor.
type NeuronMonitorSettings struct {
	MonitorSettings
}

// RuntimeMonitorSettings holds settings for the runtime monitor.
type RuntimeMonitorSettings struct {
	MonitorSettings
}

// MonitorsConfig holds per-monitor configuration, with a dedicated field per monitor.
type MonitorsConfig struct {
	Kernel     KernelMonitorSettings     `yaml:"kernel-monitor,omitempty" json:"kernel-monitor,omitempty"`
	Networking NetworkingMonitorSettings `yaml:"networking,omitempty" json:"networking,omitempty"`
	Storage    StorageMonitorSettings    `yaml:"storage-monitor,omitempty" json:"storage-monitor,omitempty"`
	Nvidia     NvidiaMonitorSettings     `yaml:"nvidia,omitempty" json:"nvidia,omitempty"`
	Neuron     NeuronMonitorSettings     `yaml:"neuron,omitempty" json:"neuron,omitempty"`
	Runtime    RuntimeMonitorSettings    `yaml:"runtime,omitempty" json:"runtime,omitempty"`
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
// Returns true if the config is nil. Panics if pluginName is not a known plugin.
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
		panic("IsMonitorEnabled: unknown plugin name: " + pluginName)
	}
}

// GetKernelSettings returns the settings for the kernel monitor.
func (mc *MonitorConfig) GetKernelSettings() KernelMonitorSettings {
	if mc == nil {
		return KernelMonitorSettings{}
	}
	return mc.Monitors.Kernel
}

// GetNetworkingSettings returns the settings for the networking monitor.
func (mc *MonitorConfig) GetNetworkingSettings() NetworkingMonitorSettings {
	if mc == nil {
		return NetworkingMonitorSettings{}
	}
	return mc.Monitors.Networking
}

// GetStorageSettings returns the settings for the storage monitor.
func (mc *MonitorConfig) GetStorageSettings() StorageMonitorSettings {
	if mc == nil {
		return StorageMonitorSettings{}
	}
	return mc.Monitors.Storage
}

// GetNvidiaSettings returns the settings for the Nvidia monitor.
func (mc *MonitorConfig) GetNvidiaSettings() NvidiaMonitorSettings {
	if mc == nil {
		return NvidiaMonitorSettings{}
	}
	return mc.Monitors.Nvidia
}

// GetNeuronSettings returns the settings for the Neuron monitor.
func (mc *MonitorConfig) GetNeuronSettings() NeuronMonitorSettings {
	if mc == nil {
		return NeuronMonitorSettings{}
	}
	return mc.Monitors.Neuron
}

// GetRuntimeSettings returns the settings for the runtime monitor.
func (mc *MonitorConfig) GetRuntimeSettings() RuntimeMonitorSettings {
	if mc == nil {
		return RuntimeMonitorSettings{}
	}
	return mc.Monitors.Runtime
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
