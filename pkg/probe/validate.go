// Package probe implements the runtime for the declarative probe contract in
// api/probe: spec validation, the transports that execute checks, and the
// runner that schedules checks and emits conditions through a parent
// monitor's manager.
package probe

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/probe"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// Names of the checks in a spec, used in error messages and to apply the
// rules that differ between them.
const (
	livenessCheck    = "liveness"
	readinessCheck   = "readiness"
	diagnosticsCheck = "diagnostics"
)

// Validate checks that a Spec is internally consistent and references only
// registered reasons and known transports. It is intended to run at startup
// so that a bad spec fails the agent loudly instead of silently never
// probing.
func Validate(spec probe.Spec) error {
	if spec.Subsystem == "" {
		return fmt.Errorf("probe spec is missing subsystem")
	}
	if err := validateChecks(spec.Checks); err != nil {
		return fmt.Errorf("probe %q: %w", spec.Subsystem, err)
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

// validateChecks validates each check in the spec. Liveness and readiness
// must report different reasons: failures are tracked per reason, so two
// checks sharing one would overwrite and resolve each other's failure.
func validateChecks(checks probe.Checks) error {
	if err := validateCheck(livenessCheck, checks.Liveness); err != nil {
		return err
	}
	if err := validateFailure(livenessCheck, checks.Liveness); err != nil {
		return err
	}
	if readiness := checks.Readiness; readiness != nil {
		if err := validateCheck(readinessCheck, *readiness); err != nil {
			return err
		}
		if err := validateFailure(readinessCheck, *readiness); err != nil {
			return err
		}
		if readiness.ReasonOnFail == checks.Liveness.ReasonOnFail {
			return fmt.Errorf("liveness and readiness both use reasonOnFail %q; each check needs its own reason", readiness.ReasonOnFail)
		}
	}
	if diagnostics := checks.Diagnostics; diagnostics != nil {
		if err := validateCheck(diagnosticsCheck, *diagnostics); err != nil {
			return err
		}
		if diagnostics.ReasonOnFail != "" || diagnostics.FailureSeverity != "" {
			return fmt.Errorf("diagnostics check has no pass or fail, so it cannot set reasonOnFail or failureSeverity")
		}
	}
	return nil
}

// validateCheck validates how a check reaches its agent: the transport,
// address, and path. Only liveness can use systemd-dbus, because a unit's
// ActiveState says whether the agent is running, not whether it is doing
// its job or what state it is in.
func validateCheck(name string, c probe.Check) error {
	if c.Address == "" {
		return fmt.Errorf("%s check is missing address", name)
	}
	switch c.Transport {
	case probe.TransportHTTPLoopback:
		if err := validateLoopbackAddress(c.Address); err != nil {
			return fmt.Errorf("%s check: %w", name, err)
		}
		if !strings.HasPrefix(c.Path, "/") {
			return fmt.Errorf("%s check: http-loopback requires a path starting with %q, got %q", name, "/", c.Path)
		}
	case probe.TransportSystemdDBus:
		if name != livenessCheck {
			return fmt.Errorf("%s check: systemd-dbus only reports whether a unit is running, so only the liveness check can use it", name)
		}
		if err := validateServiceUnit(c.Address); err != nil {
			return fmt.Errorf("%s check: %w", name, err)
		}
		if c.Path != "" {
			return fmt.Errorf("%s check: systemd-dbus does not use a path, got %q", name, c.Path)
		}
	default:
		return fmt.Errorf("%s check: unknown transport %q", name, c.Transport)
	}
	return nil
}

// validateLoopbackAddress requires localhost or a loopback IP, and a port,
// so an http-loopback check never leaves the node. Other hostnames are
// rejected because they could resolve to an address off the node.
func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("http-loopback address %q must be host:port: %w", address, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("http-loopback address %q must use localhost or a loopback IP such as 127.0.0.1 or [::1]", address)
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return fmt.Errorf("http-loopback address %q has an invalid port", address)
	}
	return nil
}

// Unit names follow systemd's own rules (unit_name_is_valid): shorter than
// maxUnitNameLength bytes, and using only unitNameChars before the type
// suffix.
const (
	maxUnitNameLength = 256
	unitNameChars     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789:-_.\\@"
)

// validateServiceUnit requires a systemd service unit name for a
// systemd-dbus check. systemd rejects an invalid name on every query, so a
// typo such as "ipamd" would leave the probe Unknown forever instead of
// failing at startup. The unit must be a service because the transport
// reads Result from systemd's Service interface.
func validateServiceUnit(unit string) error {
	prefix, ok := strings.CutSuffix(unit, ".service")
	invalidChar := func(r rune) bool { return !strings.ContainsRune(unitNameChars, r) }
	if !ok || prefix == "" || prefix[0] == '@' || len(unit) >= maxUnitNameLength || strings.ContainsFunc(prefix, invalidChar) {
		return fmt.Errorf("systemd-dbus address %q must be a systemd service unit name such as ipamd.service", unit)
	}
	// systemd only looks at the first "@". A name that ends with it is a
	// template, which has no instance to query.
	if strings.IndexByte(prefix, '@') == len(prefix)-1 {
		return fmt.Errorf("systemd-dbus address %q is a template unit with no instance", unit)
	}
	return nil
}

// validateFailure validates what a check reports when it fails: a
// registered, non-parameterized reason and, if set, a known severity.
func validateFailure(name string, c probe.Check) error {
	if c.ReasonOnFail == "" {
		return fmt.Errorf("%s check is missing reasonOnFail", name)
	}
	meta, ok := reasons.ByName(c.ReasonOnFail)
	if !ok {
		return fmt.Errorf("%s check: reasonOnFail %q is not a registered reason", name, c.ReasonOnFail)
	}
	if strings.Contains(meta.Template(), "%") {
		return fmt.Errorf("%s check: reasonOnFail %q has a parameterized template %q and cannot be used by a probe", name, c.ReasonOnFail, meta.Template())
	}
	switch c.FailureSeverity {
	case "", monitor.SeverityInfo, monitor.SeverityWarning, monitor.SeverityFatal:
	default:
		return fmt.Errorf("%s check: invalid failureSeverity %q", name, c.FailureSeverity)
	}
	return nil
}
