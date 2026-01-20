package examples

// This example shows how the internal EKSNodeMonitoringAgent package
// will integrate with the public upstream package

import (
	"context"

	"golang.a2z.com/Eks-node-monitoring-agent/api"
	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/framework"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

// Example: How internal package would register monitors in main.go
func InternalPackageIntegration(ctx context.Context, monitorManager interface{}) error {
	// Step 1: Create internal monitors (these would be actual implementations)
	internalMonitors := []monitor.Monitor{
		// &kernel.KernelMonitor{},
		// &storage.StorageMonitor{},
		// &networking.NetworkingMonitor{},
		// &runtime.RuntimeMonitor{},
		// &nvidia.NvidiaMonitor{},
	}

	// Step 2: Get CRDs from upstream
	upstreamCRDs := api.CRDs

	// Step 3: Create plugin with monitors and CRDs
	plugin := framework.NewPluginWithCRDs(
		"eks-internal-monitors",
		internalMonitors,
		upstreamCRDs,
	)

	// Step 4: Register plugin
	if err := registry.ValidateAndRegister(plugin); err != nil {
		return err
	}

	// Step 5: Get all monitors from registry
	allMonitors := registry.GlobalRegistry().AllMonitors()

	// Step 6: Register each monitor with MonitorManager
	// (This replaces the hardcoded monitorSettings.Add() calls)
	for _, mon := range allMonitors {
		conditionType := getConditionTypeForMonitor(mon.Name())
		// monitorManager.Register(ctx, mon, conditionType)
		_ = mon
		_ = conditionType
	}

	// Step 7: Get CRDs for installation
	allCRDs := registry.GlobalRegistry().AllCRDs()
	_ = allCRDs // These would be installed to the cluster

	return nil
}

// Helper to map monitor names to condition types
// (Internal package would implement this based on their rules)
func getConditionTypeForMonitor(monitorName string) string {
	mapping := map[string]string{
		"runtime":    "ContainerRuntimeReady",
		"storage":    "StorageReady",
		"networking": "NetworkingReady",
		"kernel":     "KernelReady",
		"nvidia":     "AcceleratedHardwareReady",
		"neuron":     "AcceleratedHardwareReady",
	}
	return mapping[monitorName]
}
