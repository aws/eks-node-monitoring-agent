package kernel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

func TestKernelMonitorAutoRegistration(t *testing.T) {
	// The init() function should have auto-registered the kernel monitor
	plugin, exists := registry.GlobalRegistry().Get("kernel-monitor")
	assert.True(t, exists, "kernel-monitor plugin should be registered")
	assert.NotNil(t, plugin, "kernel-monitor plugin should not be nil")

	monitors := plugin.Monitors()
	assert.Len(t, monitors, 1, "kernel-monitor plugin should have exactly one monitor")
	assert.Equal(t, "kernel", monitors[0].Name(), "monitor name should be 'kernel'")
}

func TestKernelMonitorInGlobalRegistry(t *testing.T) {
	// Verify the kernel monitor appears in the global registry's AllMonitors
	allMonitors := registry.GlobalRegistry().AllMonitors()

	var foundKernelMonitor bool
	for _, mon := range allMonitors {
		if mon.Name() == "kernel" {
			foundKernelMonitor = true
			break
		}
	}

	assert.True(t, foundKernelMonitor, "kernel monitor should be in global registry's AllMonitors()")
}
