package examples

// This example shows how to use the framework to pull both monitors and CRDs

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api"
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/framework"
	"github.com/aws/eks-node-monitoring-agent/pkg/monitor/registry"
)

// Example 1: Creating a plugin with CRDs
func CreatePluginWithCRDs() registry.MonitorPlugin {
	// Get CRDs from upstream
	crds := api.CRDs

	// Create monitors
	monitors := []monitor.Monitor{
		// Your monitors here
	}

	// Create plugin with both monitors and CRDs
	return framework.NewPluginWithCRDs("my-plugin", monitors, crds)
}

// Example 2: Registering and using CRDs
func RegisterAndUseCRDs() error {
	// Create and register plugin
	plugin := CreatePluginWithCRDs()
	if err := registry.ValidateAndRegister(plugin); err != nil {
		return err
	}

	// Get all CRDs from all registered plugins
	allCRDs := registry.GlobalRegistry().AllCRDs()

	fmt.Printf("Found %d CRDs from upstream\n", len(allCRDs))
	for _, crd := range allCRDs {
		fmt.Printf("  - %s\n", crd.Name)
	}

	return nil
}

// Example 3: Complete integration with monitors and CRDs
func CompleteIntegration() error {
	// 1. Get upstream CRDs
	upstreamCRDs := api.CRDs

	// 2. Create internal monitors
	internalMonitors := []monitor.Monitor{
		// Internal monitor implementations
	}

	// 3. Create plugin with both
	plugin := framework.NewPluginWithCRDs(
		"internal-plugin",
		internalMonitors,
		upstreamCRDs,
	)

	// 4. Register
	if err := registry.ValidateAndRegister(plugin); err != nil {
		return fmt.Errorf("failed to register plugin: %w", err)
	}

	// 5. Get everything from registry
	allMonitors := registry.GlobalRegistry().AllMonitors()
	allCRDs := registry.GlobalRegistry().AllCRDs()

	fmt.Printf("Registered %d monitors and %d CRDs\n", len(allMonitors), len(allCRDs))

	// 6. Use monitors with MonitorManager
	for _, mon := range allMonitors {
		// monitorManager.Register(ctx, mon, conditionType)
		_ = mon
	}

	// 7. Install CRDs to cluster
	for _, crd := range allCRDs {
		// Install CRD using your preferred method
		fmt.Printf("CRD available: %s\n", crd.Name)
	}

	return nil
}
