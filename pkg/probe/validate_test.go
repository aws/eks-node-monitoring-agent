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
				Transport: probe.TransportSystemdDBus,
				Address:   "ipamd.service",
			},
		},
		ReasonOnFail:       "IPAMDNotRunning",
		FailureSeverity:    monitor.SeverityFatal,
		Interval:           metav1.Duration{Duration: 30 * time.Second},
		FailureThreshold:   3,
		StartupGracePeriod: metav1.Duration{Duration: 5 * time.Minute},
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
				s.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/healthz"}
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173", Path: "/readyz"}
			},
		},
		{
			name:   "empty failureSeverity defaults later and is valid",
			mutate: func(s *probe.Spec) { s.FailureSeverity = "" },
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
				s.Checks.Liveness = probe.Check{Transport: probe.TransportHTTPLoopback, Address: "127.0.0.1:8173"}
			},
			wantErr: "requires a path",
		},
		{
			name:    "dbus check with path",
			mutate:  func(s *probe.Spec) { s.Checks.Liveness.Path = "/healthz" },
			wantErr: "does not use a path",
		},
		{
			name: "invalid readiness check",
			mutate: func(s *probe.Spec) {
				s.Checks.Readiness = &probe.Check{Transport: probe.TransportHTTPLoopback, Address: ""}
			},
			wantErr: "readiness check is missing address",
		},
		{
			name:    "unregistered reason",
			mutate:  func(s *probe.Spec) { s.ReasonOnFail = "NoSuchReason" },
			wantErr: "not a registered reason",
		},
		{
			name:    "parameterized reason template",
			mutate:  func(s *probe.Spec) { s.ReasonOnFail = "NvidiaXIDError" },
			wantErr: "parameterized template",
		},
		{
			name:    "invalid severity",
			mutate:  func(s *probe.Spec) { s.FailureSeverity = "Critical" },
			wantErr: "invalid failureSeverity",
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
