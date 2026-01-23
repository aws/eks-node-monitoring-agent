package registry

import (
	"fmt"
	"sync"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

type pluginRegistry struct {
	mu      sync.RWMutex
	plugins map[string]MonitorPlugin
}

// NewRegistry creates a new plugin registry
func NewRegistry() Registry {
	return &pluginRegistry{
		plugins: make(map[string]MonitorPlugin),
	}
}

// Register adds a plugin to the registry
func (r *pluginRegistry) Register(plugin MonitorPlugin) error {
	if plugin == nil {
		return fmt.Errorf("cannot register nil plugin")
	}

	name := plugin.Name()
	if name == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin %q already registered", name)
	}

	r.plugins[name] = plugin
	return nil
}

// Get retrieves a plugin by name
func (r *pluginRegistry) Get(name string) (MonitorPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plugin, ok := r.plugins[name]
	return plugin, ok
}

// List returns all registered plugins
func (r *pluginRegistry) List() []MonitorPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plugins := make([]MonitorPlugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		plugins = append(plugins, p)
	}
	return plugins
}

// AllMonitors returns all monitors from all registered plugins
func (r *pluginRegistry) AllMonitors() []monitor.Monitor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var monitors []monitor.Monitor
	for _, plugin := range r.plugins {
		monitors = append(monitors, plugin.Monitors()...)
	}
	return monitors
}

// AllCRDs returns all CRDs from plugins that provide them
func (r *pluginRegistry) AllCRDs() []*apiextensionsv1.CustomResourceDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var crds []*apiextensionsv1.CustomResourceDefinition
	for _, plugin := range r.plugins {
		if crdProvider, ok := plugin.(CRDProvider); ok {
			crds = append(crds, crdProvider.CRDs()...)
		}
	}
	return crds
}

// Global registry instance
var globalRegistry = NewRegistry()

// GlobalRegistry returns the global plugin registry
func GlobalRegistry() Registry {
	return globalRegistry
}

// Register is a convenience function to register a plugin globally
func Register(plugin MonitorPlugin) error {
	return globalRegistry.Register(plugin)
}

// MustRegister registers a plugin or panics on error
func MustRegister(plugin MonitorPlugin) {
	if err := Register(plugin); err != nil {
		panic(fmt.Sprintf("failed to register plugin: %v", err))
	}
}
