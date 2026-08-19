package networking

import (
	"testing"

	apiprobe "github.com/aws/eks-node-monitoring-agent/api/probe"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/probe"
)

func TestProbeSpecs_EmptyOffAutoMode(t *testing.T) {
	mon := NewNetworkingMonitor(WithRuntimeContext(&config.RuntimeContext{}))
	if specs := mon.probeSpecs(); len(specs) != 0 {
		t.Fatalf("expected no probes off Auto Mode, got %+v", specs)
	}
}

func TestProbeSpecs_AutoModeIPAMDLiveness(t *testing.T) {
	rc := &config.RuntimeContext{}
	rc.AddTags(config.EKSAuto)
	mon := NewNetworkingMonitor(WithRuntimeContext(rc))

	specs := mon.probeSpecs()
	if len(specs) != 1 {
		t.Fatalf("expected exactly the IPAMD probe on Auto Mode, got %+v", specs)
	}
	spec := specs[0]
	if spec.Subsystem != "ipamd" || spec.ReasonOnFail != "IPAMDNotRunning" {
		t.Errorf("unexpected identity: %+v", spec)
	}
	if spec.Checks.Liveness.Transport != apiprobe.TransportSystemdDBus || spec.Checks.Liveness.Address != "ipamd.service" {
		t.Errorf("unexpected liveness check: %+v", spec.Checks.Liveness)
	}

	// Every shipped spec must pass the same startup validation the runner
	// applies, including the reason taxonomy lookup.
	if err := probe.Validate(spec); err != nil {
		t.Errorf("shipped probe spec is invalid: %v", err)
	}
}
