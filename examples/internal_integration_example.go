package examples

// This example shows how the internal EKSNodeMonitoringAgent package
// will integrate with the public upstream package using the plugin architecture
// to simplify monitor creation and registration.

import (
	"context"

	"golang.a2z.com/Eks-node-monitoring-agent/api"
	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/framework"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
)

// Example 1: Simple plugin creation
func SimplePluginCreation() error {
	monitors := []monitor.Monitor{
		// &kernel.KernelMonitor{},
		// &storage.StorageMonitor{},
	}

	myPlugin := framework.NewPlugin("my-monitors", monitors)
	return registry.Register(myPlugin)
}

// Example 2: Plugin with CRDs
func PluginWithCRDs() error {
	monitors := []monitor.Monitor{
		// Your monitors
	}

	myPlugin := framework.NewPluginWithCRDs("my-plugin-with-crds", monitors, api.CRDs)
	return registry.ValidateAndRegister(myPlugin)
}

// Example 3: How internal package would register monitors in main.go (BEFORE)
// This shows the OLD hardcoded approach that we're replacing
func InternalPackageIntegrationOldWay(ctx context.Context, monitorManager interface{}) error {
	// OLD WAY: Hardcoded monitor instantiation
	// monitorSettings.Add(
	//     runtime.NewRuntimeMonitor(...),
	//     rules.ContainerRuntimeReady,
	//     "ContainerRuntimeIsReady",
	//     "Monitoring for the ContainerRuntime system is active",
	// )
	// monitorSettings.Add(storage.NewStorageMonitor(), ...)
	// monitorSettings.Add(networking.NewNetworkingMonitor(), ...)
	// ... etc for each monitor

	return nil
}

// Example 4: How internal package would register monitors in main.go (AFTER)
// This shows the NEW plugin-based approach
func InternalPackageIntegration(ctx context.Context, monitorManager interface{}) error {
	// Step 1: Create core monitors plugin
	coreMonitors := []monitor.Monitor{
		// &kernel.KernelMonitor{},
		// &storage.StorageMonitor{},
		// &networking.NetworkingMonitor{},
		// &runtime.RuntimeMonitor{},
	}

	corePlugin := framework.NewPluginWithCRDs(
		"core-monitors",
		coreMonitors,
		api.CRDs,
	)

	// Step 2: Create accelerated hardware plugin (conditional)
	var hwMonitors []monitor.Monitor
	// if runtimeContext.AcceleratedHardware() == config.AcceleratedHardwareNvidia {
	//     hwMonitors = append(hwMonitors, &nvidia.NvidiaMonitor{})
	// }
	// if runtimeContext.AcceleratedHardware() == config.AcceleratedHardwareNeuron {
	//     hwMonitors = append(hwMonitors, &neuron.NeuronMonitor{})
	// }

	var hwPlugin registry.MonitorPlugin
	if len(hwMonitors) > 0 {
		hwPlugin = framework.NewPlugin("accelerated-hardware", hwMonitors)
	}

	// Step 3: Register plugins
	if err := registry.ValidateAndRegister(corePlugin); err != nil {
		return err
	}
	if hwPlugin != nil {
		if err := registry.ValidateAndRegister(hwPlugin); err != nil {
			return err
		}
	}

	// Step 4: Get all monitors from registry and register with manager
	conditionMappings := getConditionMappings()
	for _, mon := range registry.GlobalRegistry().AllMonitors() {
		mapping := conditionMappings[mon.Name()]
		// monitorManager.Register(ctx, mon, mapping.ConditionType)
		_ = mon
		_ = mapping
	}

	return nil
}

// Example 5: Complete integration showing plugin creation and registration
func InternalPackageIntegrationComplete(ctx context.Context) error {
	// Create core monitors plugin
	coreMonitors := []monitor.Monitor{
		// &kernel.KernelMonitor{},
		// &storage.StorageMonitor{},
	}
	corePlugin := framework.NewPluginWithCRDs("core-monitors", coreMonitors, api.CRDs)

	// Register
	if err := registry.ValidateAndRegister(corePlugin); err != nil {
		return err
	}

	return nil
}

// ConditionMapping maps monitor names to their condition configurations
type ConditionMapping struct {
	ConditionType string
	ReadyReason   string
	ReadyMessage  string
}

// Helper to map monitor names to condition configurations
// (Internal package would implement this based on their rules)
func getConditionMappings() map[string]ConditionMapping {
	return map[string]ConditionMapping{
		"runtime": {
			ConditionType: "ContainerRuntimeReady",
			ReadyReason:   "ContainerRuntimeIsReady",
			ReadyMessage:  "Monitoring for the ContainerRuntime system is active",
		},
		"storage": {
			ConditionType: "StorageReady",
			ReadyReason:   "DiskIsReady",
			ReadyMessage:  "Monitoring for the Disk system is active",
		},
		"networking": {
			ConditionType: "NetworkingReady",
			ReadyReason:   "NetworkingIsReady",
			ReadyMessage:  "Monitoring for the Networking system is active",
		},
		"kernel": {
			ConditionType: "KernelReady",
			ReadyReason:   "KernelIsReady",
			ReadyMessage:  "Monitoring for the Kernel system is active",
		},
		"nvidia": {
			ConditionType: "AcceleratedHardwareReady",
			ReadyReason:   "NvidiaAcceleratedHardwareIsReady",
			ReadyMessage:  "Monitoring for the Nvidia AcceleratedHardware system is active",
		},
		"neuron": {
			ConditionType: "AcceleratedHardwareReady",
			ReadyReason:   "NeuronAcceleratedHardwareIsReady",
			ReadyMessage:  "Monitoring for the Neuron AcceleratedHardware system is active",
		},
	}
}
