package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

// fakeDBusConn implements dbusConn with canned unit properties. Like
// systemd, it keeps the Unit and Service interfaces apart: asking one
// interface for a property only the other has is an error.
type fakeDBusConn struct {
	unitProps    map[string]any // org.freedesktop.systemd1.Unit properties
	serviceProps map[string]any // org.freedesktop.systemd1.Service properties
	propErr      error
	closed       bool
}

func (f *fakeDBusConn) GetUnitPropertyContext(_ context.Context, _ string, propertyName string) (*dbus.Property, error) {
	return f.property("org.freedesktop.systemd1.Unit", f.unitProps, propertyName)
}

func (f *fakeDBusConn) GetServicePropertyContext(_ context.Context, _ string, propertyName string) (*dbus.Property, error) {
	return f.property("org.freedesktop.systemd1.Service", f.serviceProps, propertyName)
}

func (f *fakeDBusConn) property(iface string, props map[string]any, name string) (*dbus.Property, error) {
	if f.propErr != nil {
		return nil, f.propErr
	}
	v, ok := props[name]
	if !ok {
		// What systemd returns for a property the interface does not have.
		return nil, godbus.NewError("org.freedesktop.DBus.Error.UnknownProperty",
			[]any{fmt.Sprintf("Unknown interface %s or property %s.", iface, name)})
	}
	return &dbus.Property{Name: name, Value: godbus.MakeVariant(v)}, nil
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
	conn := &fakeDBusConn{unitProps: map[string]any{"ActiveState": "active"}}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("outcome = %q (%s), want Healthy", result.Outcome, result.Detail)
	}
	if !conn.closed {
		t.Error("connection was not closed")
	}
}

func TestSystemdDBusTransport_InactiveWithEnrichment(t *testing.T) {
	conn := &fakeDBusConn{
		unitProps: map[string]any{
			"ActiveState": "failed",
			"LoadState":   "loaded",
			"SubState":    "auto-restart",
		},
		serviceProps: map[string]any{"Result": "exit-code"},
	}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q, want Unhealthy", result.Outcome)
	}
	for _, want := range []string{`ActiveState="failed"`, `LoadState="loaded"`, `SubState="auto-restart"`, `Result="exit-code"`, "ipamd.service"} {
		if !strings.Contains(result.Detail, want) {
			t.Errorf("detail %q should contain %q", result.Detail, want)
		}
	}
}

func TestSystemdDBusTransport_MissingUnitIsUnhealthy(t *testing.T) {
	// systemd answers for a unit that does not exist instead of returning an
	// error: the unit is inactive and its LoadState is "not-found".
	conn := &fakeDBusConn{
		unitProps: map[string]any{
			"ActiveState": "inactive",
			"LoadState":   "not-found",
			"SubState":    "dead",
		},
		serviceProps: map[string]any{"Result": "success"},
	}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q (%s), want Unhealthy for a unit that does not exist", result.Outcome, result.Detail)
	}
	if !strings.Contains(result.Detail, `LoadState="not-found"`) {
		t.Errorf("detail %q should say the unit was not found", result.Detail)
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
	conn := &fakeDBusConn{unitProps: map[string]any{"ActiveState": uint32(7)}}
	result := dbusTransport(conn, nil).Do(context.Background(), dbusCheck())
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("outcome = %q, want Unknown for unexpected property type", result.Outcome)
	}
	if !strings.Contains(result.Detail, "uint32 7") {
		t.Errorf("detail %q should name the type and value it got", result.Detail)
	}
}
