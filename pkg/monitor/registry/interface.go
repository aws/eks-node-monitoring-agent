package registry

import (
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// MonitorPlugin represents a pluggable monitoring component
type MonitorPlugin interface {
	// Name returns a unique identifier for the plugin
	Name() string
	// Monitors returns all monitors provided by this plugin
	Monitors() []monitor.Monitor
}

// CRDProvider optionally provides CRDs that should be installed
type CRDProvider interface {
	// CRDs returns CRDs that this plugin requires
	CRDs() []*apiextensionsv1.CustomResourceDefinition
}

// Registry manages monitor plugin registration
type Registry interface {
	// Register adds a plugin to the registry
	Register(plugin MonitorPlugin) error
	// Get retrieves a plugin by name
	Get(name string) (MonitorPlugin, bool)
	// List returns all registered plugins
	List() []MonitorPlugin
	// AllMonitors returns all monitors from all plugins
	AllMonitors() []monitor.Monitor
	// AllCRDs returns all CRDs from plugins that provide them
	AllCRDs() []*apiextensionsv1.CustomResourceDefinition
}
