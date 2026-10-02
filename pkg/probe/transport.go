package probe

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

// Outcome classifies the result of executing a single check.
type Outcome string

const (
	// OutcomeHealthy means the check executed and the agent reported healthy.
	OutcomeHealthy Outcome = "Healthy"
	// OutcomeUnhealthy means the check executed and the agent is not healthy.
	// A refused connection to the agent's own endpoint counts as unhealthy:
	// an agent that is not answering its health surface is not healthy.
	OutcomeUnhealthy Outcome = "Unhealthy"
	// OutcomeUnknown means the check could not be executed for reasons on the
	// monitoring side (e.g. the D-Bus socket is unreachable). Unknown results
	// do not count toward a probe's failure threshold.
	OutcomeUnknown Outcome = "Unknown"
)

// Result is the outcome of one check execution plus human-readable detail
// for condition messages and logs.
type Result struct {
	Outcome Outcome
	Detail  string
}

// Transport executes a single check against its target.
type Transport interface {
	Do(ctx context.Context, check probe.Check) Result
}

// NewTransports returns the default transport for each supported kind.
func NewTransports() map[probe.TransportKind]Transport {
	return map[probe.TransportKind]Transport{
		probe.TransportHTTPLoopback: NewHTTPLoopbackTransport(),
		probe.TransportSystemdDBus:  NewSystemdDBusTransport(),
	}
}
