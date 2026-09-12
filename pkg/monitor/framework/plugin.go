package framework

import (
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Plugin provides a basic plugin implementation
type Plugin struct {
	name             string
	monitors         []monitor.Monitor
	crds             []*apiextensionsv1.CustomResourceDefinition
	conditionType    corev1.NodeConditionType
	conditionConfig  conditions.NodeConditionConfig
	hasCondition     bool
	requiredHardware string
}

// NewPlugin creates a new plugin
func NewPlugin(name string, monitors []monitor.Monitor) *Plugin {
	return &Plugin{
		name:     name,
		monitors: monitors,
		crds:     []*apiextensionsv1.CustomResourceDefinition{},
	}
}

// NewPluginWithCRDs creates a new plugin with CRDs
func NewPluginWithCRDs(name string, monitors []monitor.Monitor, crds []*apiextensionsv1.CustomResourceDefinition) *Plugin {
	return &Plugin{
		name:     name,
		monitors: monitors,
		crds:     crds,
	}
}

// WithNodeCondition declares the node condition the plugin owns
func (p *Plugin) WithNodeCondition(condType corev1.NodeConditionType, config conditions.NodeConditionConfig) *Plugin {
	p.conditionType = condType
	p.conditionConfig = config
	p.hasCondition = true
	return p
}

// WithRequiredHardware declares that the plugin only runs on the given
// accelerated hardware
func (p *Plugin) WithRequiredHardware(hardware string) *Plugin {
	p.requiredHardware = hardware
	return p
}

// Name returns the plugin name
func (p *Plugin) Name() string {
	return p.name
}

// Monitors returns all monitors provided by this plugin
func (p *Plugin) Monitors() []monitor.Monitor {
	return p.monitors
}

// CRDs returns all CRDs provided by this plugin
func (p *Plugin) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return p.crds
}

// NodeCondition implements registry.NodeConditionProvider
func (p *Plugin) NodeCondition() (corev1.NodeConditionType, conditions.NodeConditionConfig, bool) {
	return p.conditionType, p.conditionConfig, p.hasCondition
}

// RequiredHardware implements registry.HardwareRequirer
func (p *Plugin) RequiredHardware() string {
	return p.requiredHardware
}
