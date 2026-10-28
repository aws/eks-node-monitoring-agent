package corefile

import (
	"context"
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/go-logr/logr"
	godbus "github.com/godbus/dbus/v5"
)

type fakeUnitConn struct {
	loadState string
	err       error
	closed    bool
}

func (f *fakeUnitConn) GetUnitPropertyContext(_ context.Context, _ string, name string) (*dbus.Property, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &dbus.Property{Name: name, Value: godbus.MakeVariant(f.loadState)}, nil
}

func (f *fakeUnitConn) Close() { f.closed = true }

func TestAgentInstalled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		conn    *fakeUnitConn
		connErr error
		want    bool
	}{
		{name: "unit loaded", conn: &fakeUnitConn{loadState: "loaded"}, want: true},
		{name: "unit not on the AMI", conn: &fakeUnitConn{loadState: "not-found"}, want: false},
		{name: "property read fails", conn: &fakeUnitConn{err: errors.New("timeout")}, want: true},
		{name: "D-Bus unreachable", connErr: errors.New("no bus"), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentInstalled(context.Background(), logr.Discard(), func(context.Context) (unitPropertyGetter, error) {
				if tc.connErr != nil {
					return nil, tc.connErr
				}
				return tc.conn, nil
			})
			if got != tc.want {
				t.Errorf("agentInstalled() = %v, want %v", got, tc.want)
			}
			if tc.conn != nil && !tc.conn.closed {
				t.Error("connection was not closed")
			}
		})
	}
}
