package registry

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
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

// NodeConditionProvider optionally declares the node condition a plugin owns.
// Implementing it lets the agent wire the condition without hardcoding
// plugin names in main.
type NodeConditionProvider interface {
	// NodeCondition returns the condition type and its ready-state config.
	// ok is false when the plugin owns no node condition.
	NodeCondition() (condType corev1.NodeConditionType, config conditions.NodeConditionConfig, ok bool)
}

// HardwareRequirer optionally declares that a plugin only runs on specific
// accelerated hardware (see config.RuntimeContext.AcceleratedHardware).
type HardwareRequirer interface {
	// RequiredHardware returns the required hardware, "" means no requirement.
	RequiredHardware() string
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
