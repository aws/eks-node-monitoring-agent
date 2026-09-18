// Package probe defines the declarative contract for subagent health probes.
//
// A probe declares how the node monitoring agent observes the health of one
// AWS-owned subagent (e.g. IPAMD, the Network Policy Agent) through the
// agent's own health surface. Probes are pure data: the framework in
// pkg/probe interprets a Spec and emits conditions through the parent
// monitor, so onboarding an agent is a spec addition rather than new
// detection code.
package probe

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

// TransportKind identifies how a Check reaches its target agent.
type TransportKind string

const (
	// TransportHTTPLoopback performs an HTTP GET against a local-only
	// address. A 2xx response is healthy; any other response, including a
	// refused connection, is unhealthy.
	TransportHTTPLoopback TransportKind = "http-loopback"
	// TransportSystemdDBus queries the ActiveState of a systemd unit over
	// D-Bus. Liveness only: "active" is healthy, anything else is unhealthy.
	// A failure to reach D-Bus itself is unknown, not unhealthy.
	TransportSystemdDBus TransportKind = "systemd-dbus"
)

// Spec declares a health probe for one AWS-owned subagent. It is fully
// JSON/YAML-serializable so that probe registration can later move from
// binary-baked defaults to the agent's configuration surface without
// reshaping the contract.
type Spec struct {
	// Subsystem identifies the target agent, e.g. "ipamd" or
	// "network-policy-agent". Used in logs and condition messages.
	Subsystem string `json:"subsystem"`

	// Checks defines the agent's health surface.
	Checks Checks `json:"checks"`

	// ReasonOnFail is the reason identifier (a key in reasons.yaml) emitted
	// when the probe fails. It must reference a registered,
	// non-parameterized reason; this is validated at startup.
	ReasonOnFail string `json:"reasonOnFail"`

	// FailureSeverity is the severity of the emitted condition on probe
	// failure. If empty, the reason's default severity from reasons.yaml is
	// used.
	FailureSeverity monitor.Severity `json:"failureSeverity,omitempty"`

	// Interval is the time between check rounds.
	Interval metav1.Duration `json:"interval"`

	// FailureThreshold is the number of consecutive unhealthy results
	// required before the failure condition is emitted. The counter resets
	// on any healthy result. Must be at least 1.
	FailureThreshold int `json:"failureThreshold"`

	// RecoveryThreshold is the number of consecutive healthy results
	// required before a previously emitted failure condition is resolved.
	// It damps condition flapping: every False→True transition resets Auto
	// Repair's wait clock, so a flapping agent would otherwise never be
	// repaired. If zero, it defaults to 1, which resolves on the first
	// healthy result and preserves the pre-hysteresis behavior.
	RecoveryThreshold int `json:"recoveryThreshold,omitempty"`

	// StartupGracePeriod suppresses failure emission for this duration after
	// the runner starts, tolerating agents that come up after the monitoring
	// agent during boot.
	StartupGracePeriod metav1.Duration `json:"startupGracePeriod,omitempty"`
}

// Checks defines the individual checks that make up a probe. Liveness is
// required; readiness and diagnostics are optional because not every
// transport can express them (systemd ActiveState has no notion of
// readiness).
type Checks struct {
	// Liveness answers "is the agent running".
	Liveness Check `json:"liveness"`
	// Readiness answers "is the agent performing its function". The depth of
	// the readiness answer is owned by the agent, not by this contract.
	Readiness *Check `json:"readiness,omitempty"`
	// Diagnostics optionally returns free-form structured detail that is
	// surfaced as informational events, never as condition flips.
	Diagnostics *Check `json:"diagnostics,omitempty"`
}

// Check is a single health question asked over a specific transport.
type Check struct {
	// Transport selects how the target is reached.
	Transport TransportKind `json:"transport"`
	// Address is the transport-specific target, e.g. "127.0.0.1:8901" for
	// http-loopback or "ipamd.service" for systemd-dbus.
	Address string `json:"address"`
	// Path is the HTTP request path, e.g. "/healthz". Required for
	// http-loopback and must be empty for systemd-dbus.
	Path string `json:"path,omitempty"`
}
