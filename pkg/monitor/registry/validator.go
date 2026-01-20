package registry

import (
	"fmt"

	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
)

// ValidatePlugin validates a plugin before registration
func ValidatePlugin(plugin MonitorPlugin) error {
	if plugin == nil {
		return fmt.Errorf("plugin cannot be nil")
	}

	if plugin.Name() == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}

	monitors := plugin.Monitors()
	if len(monitors) == 0 {
		return fmt.Errorf("plugin must provide at least one monitor")
	}

	for i, mon := range monitors {
		if err := ValidateMonitor(mon); err != nil {
			return fmt.Errorf("monitor %d validation failed: %w", i, err)
		}
	}

	return nil
}

// ValidateMonitor validates a monitor
func ValidateMonitor(mon monitor.Monitor) error {
	if mon == nil {
		return fmt.Errorf("monitor cannot be nil")
	}

	if mon.Name() == "" {
		return fmt.Errorf("monitor name cannot be empty")
	}

	// Validate conditions
	conditions := mon.Conditions()
	for i, cond := range conditions {
		if err := ValidateCondition(cond); err != nil {
			return fmt.Errorf("condition %d validation failed: %w", i, err)
		}
	}

	return nil
}

// ValidateCondition validates a condition
func ValidateCondition(cond monitor.Condition) error {
	if cond.Reason == "" {
		return fmt.Errorf("condition reason cannot be empty")
	}

	switch cond.Severity {
	case monitor.SeverityInfo, monitor.SeverityWarning, monitor.SeverityFatal:
		// Valid severity
	default:
		return fmt.Errorf("invalid severity: %s", cond.Severity)
	}

	if cond.MinOccurrences < 0 {
		return fmt.Errorf("MinOccurrences cannot be negative")
	}

	return nil
}

// ValidateAndRegister validates a plugin before registering it
func ValidateAndRegister(plugin MonitorPlugin) error {
	if err := ValidatePlugin(plugin); err != nil {
		return fmt.Errorf("plugin validation failed: %w", err)
	}
	return Register(plugin)
}
