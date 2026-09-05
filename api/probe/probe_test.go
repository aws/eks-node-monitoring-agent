package probe

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func makeSpec() Spec {
	return Spec{
		Subsystem: "ipamd",
		Checks: Checks{
			Liveness: Check{
				Transport: TransportSystemdDBus,
				Address:   "ipamd.service",
			},
			Readiness: &Check{
				Transport: TransportHTTPLoopback,
				Address:   "127.0.0.1:8173",
				Path:      "/readyz",
			},
		},
		ReasonOnFail:       "IPAMDNotRunning",
		FailureSeverity:    monitor.SeverityFatal,
		Interval:           metav1.Duration{Duration: 30 * time.Second},
		FailureThreshold:   3,
		StartupGracePeriod: metav1.Duration{Duration: 5 * time.Minute},
	}
}

func TestSpecJSONRoundTrip(t *testing.T) {
	in := makeSpec()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Spec
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Interval.Duration != 30*time.Second {
		t.Errorf("interval = %v, want 30s", out.Interval.Duration)
	}
	if out.Checks.Readiness == nil || out.Checks.Readiness.Path != "/readyz" {
		t.Errorf("readiness check did not round-trip: %+v", out.Checks.Readiness)
	}
	if out.Subsystem != in.Subsystem || out.ReasonOnFail != in.ReasonOnFail || out.FailureThreshold != in.FailureThreshold {
		t.Errorf("spec did not round-trip: got %+v, want %+v", out, in)
	}
}

func TestSpecYAMLHumanReadableDurations(t *testing.T) {
	// The eventual configuration surface is YAML; durations must parse from
	// human-readable strings like "30s", not integer nanoseconds.
	in := []byte(`
subsystem: network-policy-agent
checks:
  liveness:
    transport: http-loopback
    address: 127.0.0.1:8901
    path: /healthz
reasonOnFail: NPANotRunning
interval: 30s
failureThreshold: 3
startupGracePeriod: 5m
`)
	var spec Spec
	if err := yaml.UnmarshalStrict(in, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.Interval.Duration != 30*time.Second {
		t.Errorf("interval = %v, want 30s", spec.Interval.Duration)
	}
	if spec.StartupGracePeriod.Duration != 5*time.Minute {
		t.Errorf("startupGracePeriod = %v, want 5m", spec.StartupGracePeriod.Duration)
	}
	if spec.Checks.Readiness != nil {
		t.Errorf("readiness should be nil when omitted, got %+v", spec.Checks.Readiness)
	}
	if spec.FailureSeverity != "" {
		t.Errorf("failureSeverity should be empty when omitted, got %q", spec.FailureSeverity)
	}
}
