package probe

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

func validSpec() probe.Spec {
	return probe.Spec{
		Subsystem: "ipamd",
		Checks: probe.Checks{
			Liveness: probe.Check{
				Transport:       probe.TransportSystemdDBus,
				Address:         "ipamd.service",
				ReasonOnFail:    "IPAMDNotRunning",
				FailureSeverity: monitor.SeverityFatal,
			},
		},
		Interval:           metav1.Duration{Duration: 30 * time.Second},
		FailureThreshold:   3,
		StartupGracePeriod: metav1.Duration{Duration: 5 * time.Minute},
	}
}

// httpLiveness switches the liveness check to http-loopback at address.
func httpLiveness(address string) func(*probe.Spec) {
	return func(s *probe.Spec) {
		s.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: address, Path: "/healthz", ReasonOnFail: "IPAMDNotRunning"}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*probe.Spec)
		wantErr string
	}{
		{
			name:   "valid systemd-dbus spec",
			mutate: func(s *probe.Spec) {},
		},
		{
			name: "valid http-loopback spec with readiness",
			mutate: func(s *probe.Spec) {
				s.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/healthz", ReasonOnFail: "IPAMDNotRunning"}
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz", ReasonOnFail: "IPAMDNotReady"}
			},
		},
		{
			name: "readiness can use a different severity than liveness",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz", ReasonOnFail: "IPAMDNotReady", FailureSeverity: monitor.SeverityWarning}
			},
		},
		{
			name:   "empty failureSeverity defaults later and is valid",
			mutate: func(s *probe.Spec) { s.Checks.Liveness.FailureSeverity = "" },
		},
		{
			name:    "missing subsystem",
			mutate:  func(s *probe.Spec) { s.Subsystem = "" },
			wantErr: "missing subsystem",
		},
		{
			name:    "missing liveness address",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = "" },
			wantErr: "liveness check is missing address",
		},
		{
			name:    "unknown transport",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Transport = "carrier-pigeon" },
			wantErr: `unknown transport "carrier-pigeon"`,
		},
		{
			name: "http check without path",
			mutate: func(s *probe.Spec) {
				s.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", ReasonOnFail: "IPAMDNotRunning"}
			},
			wantErr: "requires a path",
		},
		{
			name:    "dbus check with path",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Path = "/healthz" },
			wantErr: "does not use a path",
		},
		{
			name:    "systemd-dbus needs a unit suffix",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = "ipamd" },
			wantErr: "must be a systemd service unit name",
		},
		{
			name:    "systemd-dbus needs a service unit",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = "ipamd.socket" },
			wantErr: "must be a systemd service unit name",
		},
		{
			name:    "systemd-dbus rejects characters systemd does not allow",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = "ipamd/agent.service" },
			wantErr: "must be a systemd service unit name",
		},
		{
			name:   "systemd-dbus accepts the longest name systemd allows",
			mutate: func(s *probe.Spec) { s.Checks.Liveness.Address = strings.Repeat("a", 247) + ".service" },
		},
		{
			name:    "systemd-dbus rejects a name too long for systemd",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = strings.Repeat("a", 248) + ".service" },
			wantErr: "must be a systemd service unit name",
		},
		{
			name:    "systemd-dbus rejects a template unit",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Address = "ipamd@.service" },
			wantErr: "template unit with no instance",
		},
		{
			name:   "systemd-dbus accepts a template instance",
			mutate: func(s *probe.Spec) { s.Checks.Liveness.Address = "ipamd@eth0.service" },
		},
		{
			name: "invalid readiness check",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: ""}
			},
			wantErr: "readiness check is missing address",
		},
		{
			name:   "http-loopback accepts the IPv6 loopback",
			mutate: httpLiveness("[::1]:8173"),
		},
		{
			name:   "http-loopback accepts localhost",
			mutate: httpLiveness("localhost:8173"),
		},
		{
			name:    "http-loopback rejects a non-loopback IP",
			mutate:  httpLiveness("10.0.0.1:8173"),
			wantErr: "must use localhost or a loopback IP",
		},
		{
			name:    "http-loopback rejects other hostnames",
			mutate:  httpLiveness("example.com:8173"),
			wantErr: "must use localhost or a loopback IP",
		},
		{
			name:    "http-loopback requires a port",
			mutate:  httpLiveness("127.0.0.1"),
			wantErr: "must be host:port",
		},
		{
			name:    "http-loopback rejects port zero",
			mutate:  httpLiveness("127.0.0.1:0"),
			wantErr: "invalid port",
		},
		{
			name: "readiness cannot use systemd-dbus",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportSystemdDBus, Address: "ipamd.service", ReasonOnFail: "IPAMDNotReady"}
			},
			wantErr: "only the liveness check can use it",
		},
		{
			name:    "missing liveness reason",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.ReasonOnFail = "" },
			wantErr: "liveness check is missing reasonOnFail",
		},
		{
			name: "missing readiness reason",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz"}
			},
			wantErr: "readiness check is missing reasonOnFail",
		},
		{
			name: "readiness reuses the liveness reason",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz", ReasonOnFail: "IPAMDNotRunning"}
			},
			wantErr: "each check needs its own reason",
		},
		{
			name:    "unregistered reason",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.ReasonOnFail = "NoSuchReason" },
			wantErr: "not a registered reason",
		},
		{
			name:    "parameterized reason template",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.ReasonOnFail = "NvidiaXIDError" },
			wantErr: "parameterized template",
		},
		{
			name:    "invalid severity",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.FailureSeverity = "Critical" },
			wantErr: "invalid failureSeverity",
		},
		{
			name: "invalid readiness severity",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz", ReasonOnFail: "IPAMDNotReady", FailureSeverity: "Critical"}
			},
			wantErr: "readiness check: invalid failureSeverity",
		},
		{
			name: "valid diagnostics check",
			mutate: func(s *probe.Spec) {
				s.Checks.Diagnostics = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/diagnostics"}
			},
		},
		{
			name: "diagnostics cannot set a reason",
			mutate: func(s *probe.Spec) {
				s.Checks.Diagnostics = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/diagnostics", ReasonOnFail: "IPAMDNotReady"}
			},
			wantErr: "cannot set reasonOnFail or failureSeverity",
		},
		{
			name: "diagnostics cannot use systemd-dbus",
			mutate: func(s *probe.Spec) {
				s.Checks.Diagnostics = &probe.Check{Transport: probe.TransportSystemdDBus, Address: "ipamd.service"}
			},
			wantErr: "only the liveness check can use it",
		},
		{
			name:    "zero interval",
			mutate:  func(s *probe.Spec) { s.Interval = metav1.Duration{} },
			wantErr: "interval must be positive",
		},
		{
			name:    "zero failure threshold",
			mutate:  func(s *probe.Spec) { s.FailureThreshold = 0 },
			wantErr: "failureThreshold must be at least 1",
		},
		{
			name:   "unset recovery threshold defaults later and is valid",
			mutate: func(s *probe.Spec) { s.RecoveryThreshold = 0 },
		},
		{
			name:    "negative recovery threshold",
			mutate:  func(s *probe.Spec) { s.RecoveryThreshold = -1 },
			wantErr: "recoveryThreshold must not be negative",
		},
		{
			name:    "negative grace period",
			mutate:  func(s *probe.Spec) { s.StartupGracePeriod = metav1.Duration{Duration: -time.Second} },
			wantErr: "must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validSpec()
			tt.mutate(&spec)
			err := Validate(spec)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want error containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}
