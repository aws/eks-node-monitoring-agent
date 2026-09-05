package probe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

// fakeDBusConn implements dbusConn with canned unit properties.
type fakeDBusConn struct {
	props   map[string]any // property name -> value
	propErr error
	closed  bool
}

func (f *fakeDBusConn) GetUnitPropertyContext(ctx context.Context, unit string, propertyName string) (*dbus.Property, error) {
	if f.propErr != nil {
		return nil, f.propErr
	}
	v, ok := f.props[propertyName]
	if !ok {
		return nil, errors.New("no such property")
	}
	return &dbus.Property{Name: propertyName, Value: godbus.MakeVariant(v)}, nil
}

func (f *fakeDBusConn) Close() { f.closed = true }

func dbusTransport(conn dbusConn, connErr error) *SystemdDBusTransport {
	return &SystemdDBusTransport{
		newConn: func(ctx context.Context) (dbusConn, error) {
			if connErr != nil {
				return nil, connErr
			}
			return conn, nil
		},
	}
}

func dbusCheck() probe.Check {
	return probe.Check{Transport: probe.TransportSystemdDBus, Address: "ipamd.service"}
}

func TestSystemdDBusTransport_Active(t *testing.T) {
	conn := &fakeDBusConn{props: map[string]any{"ActiveState": "active"}}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("outcome = %q (%s), want Healthy", result.Outcome, result.Detail)
	}
	if !conn.closed {
		t.Error("connection was not closed")
	}
}

func TestSystemdDBusTransport_InactiveWithEnrichment(t *testing.T) {
	conn := &fakeDBusConn{props: map[string]any{
		"ActiveState": "failed",
		"SubState":    "auto-restart",
		"Result":      "exit-code",
	}}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q, want Unhealthy", result.Outcome)
	}
	for _, want := range []string{`ActiveState="failed"`, `SubState="auto-restart"`, `Result="exit-code"`, "ipamd.service"} {
		if !strings.Contains(result.Detail, want) {
			t.Errorf("detail %q should contain %q", result.Detail, want)
		}
	}
}

func TestSystemdDBusTransport_ConnectionErrorIsUnknown(t *testing.T) {
	result := dbusTransport(nil, errors.New("socket unavailable")).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("outcome = %q, want Unknown for D-Bus connection failure", result.Outcome)
	}
}

func TestSystemdDBusTransport_PropertyErrorIsUnknown(t *testing.T) {
	conn := &fakeDBusConn{propErr: errors.New("dbus timeout")}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("outcome = %q, want Unknown for property query failure", result.Outcome)
	}
	if !conn.closed {
		t.Error("connection was not closed")
	}
}

func TestSystemdDBusTransport_UnexpectedTypeIsUnknown(t *testing.T) {
	conn := &fakeDBusConn{props: map[string]any{"ActiveState": uint32(7)}}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("outcome = %q, want Unknown for unexpected property type", result.Outcome)
	}
}
