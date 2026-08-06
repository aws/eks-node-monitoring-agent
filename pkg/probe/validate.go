// Package probe implements the runtime for the declarative probe contract in
// api/probe: spec validation, the transports that execute checks, and the
// runner that schedules checks and emits conditions through a parent
// monitor's manager.
package probe

import (
	"fmt"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/probe"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// Validate checks that a Spec is internally consistent and references only
// registered reasons and known transports. It is intended to run at startup
// so that a bad spec fails the agent loudly instead of silently never
// probing.
func Validate(spec probe.Spec) error {
	if spec.Subsystem == "" {
		return fmt.Errorf("probe spec is missing subsystem")
	}
	if err := validateCheck("liveness", spec.Checks.Liveness); err != nil {
		return fmt.Errorf("probe %q: %w", spec.Subsystem, err)
	}
	if spec.Checks.Readiness != nil {
		if err := validateCheck("readiness", *spec.Checks.Readiness); err != nil {
			return fmt.Errorf("probe %q: %w", spec.Subsystem, err)
		}
	}
	if spec.Checks.Diagnostics != nil {
		if err := validateCheck("diagnostics", *spec.Checks.Diagnostics); err != nil {
			return fmt.Errorf("probe %q: %w", spec.Subsystem, err)
		}
	}

	meta, ok := reasons.ByName(spec.ReasonOnFail)
	if !ok {
		return fmt.Errorf("probe %q: reasonOnFail %q is not a registered reason", spec.Subsystem, spec.ReasonOnFail)
	}
	if strings.Contains(meta.Template(), "%") {
		return fmt.Errorf("probe %q: reasonOnFail %q has a parameterized template %q and cannot be used by a probe", spec.Subsystem, spec.ReasonOnFail, meta.Template())
	}

	switch spec.FailureSeverity {
	case "", monitor.SeverityInfo, monitor.SeverityWarning, monitor.SeverityFatal:
	default:
		return fmt.Errorf("probe %q: invalid failureSeverity %q", spec.Subsystem, spec.FailureSeverity)
	}

	if spec.Interval.Duration <= 0 {
		return fmt.Errorf("probe %q: interval must be positive, got %v", spec.Subsystem, spec.Interval.Duration)
	}
	if spec.FailureThreshold < 1 {
		return fmt.Errorf("probe %q: failureThreshold must be at least 1, got %d", spec.Subsystem, spec.FailureThreshold)
	}
	if spec.RecoveryThreshold < 0 {
		return fmt.Errorf("probe %q: recoveryThreshold must not be negative, got %d", spec.Subsystem, spec.RecoveryThreshold)
	}
	if spec.StartupGracePeriod.Duration < 0 {
		return fmt.Errorf("probe %q: startupGracePeriod must not be negative, got %v", spec.Subsystem, spec.StartupGracePeriod.Duration)
	}
	return nil
}

// validateCheck validates a single check's transport, address, and path.
func validateCheck(name string, c probe.Check) error {
	if c.Address == "" {
		return fmt.Errorf("%s check is missing address", name)
	}
	switch c.Transport {
	case probe.TransportHTTPLoopback:
		if !strings.HasPrefix(c.Path, "/") {
			return fmt.Errorf("%s check: http-loopback requires a path starting with %q, got %q", name, "/", c.Path)
		}
	case probe.TransportSystemdDBus:
		if c.Path != "" {
			return fmt.Errorf("%s check: systemd-dbus does not use a path, got %q", name, c.Path)
		}
	default:
		return fmt.Errorf("%s check: unknown transport %q", name, c.Transport)
	}
	return nil
}
