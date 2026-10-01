package probe

import (
	"context"
	"fmt"

	"github.com/coreos/go-systemd/v22/dbus"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

// dbusConn is the subset of the go-systemd dbus connection used by the
// transport, extracted so tests can substitute a fake.
type dbusConn interface {
	GetUnitPropertyContext(ctx context.Context, unit string, propertyName string) (*dbus.Property, error)
	GetServicePropertyContext(ctx context.Context, service string, propertyName string) (*dbus.Property, error)
	Close()
}

// SystemdDBusTransport executes liveness checks by querying a systemd
// service's ActiveState over D-Bus. It generalizes the ActiveState query used
// by the networking monitor's IPAMD and NPA handlers. Failure to reach D-Bus
// itself is Unknown, not Unhealthy: the inability to ask the question is not
// evidence about the agent. A unit that does not exist is not an error:
// systemd reports it inactive with LoadState "not-found", so it is unhealthy.
type SystemdDBusTransport struct {
	newConn func(ctx context.Context) (dbusConn, error)
}

// NewSystemdDBusTransport returns a transport backed by real D-Bus
// connections.
func NewSystemdDBusTransport() *SystemdDBusTransport {
	return &SystemdDBusTransport{
		newConn: func(ctx context.Context) (dbusConn, error) {
			return dbus.NewWithContext(ctx)
		},
	}
}

func (t *SystemdDBusTransport) Do(ctx context.Context, check probe.Check) Result {
	conn, err := t.newConn(ctx)
	if err != nil {
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("connecting to D-Bus: %v", err)}
	}
	defer conn.Close()

	property, err := conn.GetUnitPropertyContext(ctx, check.Address, "ActiveState")
	if err != nil {
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("querying ActiveState of %s: %v", check.Address, err)}
	}
	value := property.Value.Value()
	activeState, ok := value.(string)
	if !ok {
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("unexpected ActiveState type for %s: got %T %v, want string", check.Address, value, value)}
	}
	if activeState == "active" {
		return Result{Outcome: OutcomeHealthy}
	}

	// Best-effort diagnostic enrichment: why/how the unit is in its state.
	// Result is defined on systemd's Service interface, not the Unit
	// interface; validation guarantees the unit is a service.
	detail := fmt.Sprintf("unit %s ActiveState=%q", check.Address, activeState)
	for _, prop := range []struct {
		name string
		get  func(ctx context.Context, unit string, propertyName string) (*dbus.Property, error)
	}{
		{"LoadState", conn.GetUnitPropertyContext},
		{"SubState", conn.GetUnitPropertyContext},
		{"Result", conn.GetServicePropertyContext},
	} {
		if p, err := prop.get(ctx, check.Address, prop.name); err == nil {
			if s, ok := p.Value.Value().(string); ok {
				detail += fmt.Sprintf(" %s=%q", prop.name, s)
			}
		}
	}
	return Result{Outcome: OutcomeUnhealthy, Detail: detail}
}
