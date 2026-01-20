package framework

import (
	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Plugin provides a basic plugin implementation
type Plugin struct {
	name     string
	monitors []monitor.Monitor
	crds     []*apiextensionsv1.CustomResourceDefinition
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
