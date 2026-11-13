package corefile

import (
	"context"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/go-logr/logr"
)

const AgentUnit = "eks-networking-config-agent.service"

const unitCheckTimeout = 5 * time.Second

type unitPropertyGetter interface {
	GetUnitPropertyContext(ctx context.Context, unit string, propertyName string) (*dbus.Property, error)
	Close()
}

// AgentInstalled reports whether the agent's unit exists. It returns true if
// systemd can't be asked, so the probe still runs.
func AgentInstalled(ctx context.Context, log logr.Logger) bool {
	return agentInstalled(ctx, log, func(ctx context.Context) (unitPropertyGetter, error) {
		return dbus.NewWithContext(ctx)
	})
}

func agentInstalled(ctx context.Context, log logr.Logger, newConn func(context.Context) (unitPropertyGetter, error)) bool {
	ctx, cancel := context.WithTimeout(ctx, unitCheckTimeout)
	defer cancel()

	conn, err := newConn(ctx)
	if err != nil {
		log.Info("could not reach systemd to check for the Corefile agent; probing it anyway", "err", err.Error())
		return true
	}
	defer conn.Close()

	property, err := conn.GetUnitPropertyContext(ctx, AgentUnit, "LoadState")
	if err != nil {
		log.Info("could not read the Corefile agent's LoadState; probing it anyway", "err", err.Error())
		return true
	}
	if state, _ := property.Value.Value().(string); state == "not-found" {
		log.Info("Corefile agent is not installed on this node; skipping its readyz probe", "unit", AgentUnit)
		return false
	}
	return true
}
