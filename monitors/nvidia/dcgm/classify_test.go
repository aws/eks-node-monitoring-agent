//go:build !darwin

package dcgm

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func reasonSeverity(conds []monitor.Condition, reason string) (monitor.Severity, bool) {
	for _, c := range conds {
		if c.Reason == reason {
			return c.Severity, true
		}
	}
	return "", false
}

// TestClassify_currentPolicy is a characterization test: it locks NMA's
// classification as it behaves today, so a future change to the signal source
// cannot silently regress it. These all pass against the current policy.
func TestClassify_currentPolicy(t *testing.T) {
	cases := []struct {
		name       string
		in         NormalizedSignals
		wantReason string // "" => expect NO condition
		wantSev    monitor.Severity
	}{
		{"well-known XID 79 => Fatal", NormalizedSignals{XIDs: []uint{79}}, "NvidiaXID79Error", monitor.SeverityFatal},
		{"non-well-known XID 13 => Warning", NormalizedSignals{XIDs: []uint{13}}, "NvidiaXID13Warning", monitor.SeverityWarning},
		{"DBE => Fatal", NormalizedSignals{DoubleBitECC: true}, "NvidiaDoubleBitError", monitor.SeverityFatal},
		{"NVLink hard error => Fatal", NormalizedSignals{NVLinkError: true}, "NvidiaNVLinkError", monitor.SeverityFatal},
		{"thermal => Warning only (documents FN1: no repair)", NormalizedSignals{ThermalViolation: true}, "NvidiaThermalError", monitor.SeverityWarning},
		{"page-retirement => Warning only (FN2)", NormalizedSignals{PageRetirement: true}, "NvidiaPageRetirement", monitor.SeverityWarning},
		{"power violation => Warning only (FN3)", NormalizedSignals{PowerViolation: true}, "NvidiaPowerError", monitor.SeverityWarning},
		{"PCIe replay => Warning only (FN4)", NormalizedSignals{PCIeReplay: true}, "NvidiaPCIeError", monitor.SeverityWarning},
		{"no signals => no condition (power/thermal violation code 0)", NormalizedSignals{}, "", ""},
		// Regression guard: 0x80 is a HEALTHY fabric mask (an earlier bug
		// treated it as fatal). Must produce no condition.
		{"fabric mask 0x80 is healthy (healthy-mask guard)", NormalizedSignals{FabricHealthMask: u64(0x80)}, "", ""},
		// A genuine fabric fault (degraded_bw asserted, bit0=1) => Fatal.
		{"fabric mask 0x1 degraded_bw => Fatal", NormalizedSignals{FabricHealthMask: u64(0x1)}, "NvidiaFabricError", monitor.SeverityFatal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			sev, found := reasonSeverity(got, tc.wantReason)
			if tc.wantReason == "" {
				if len(got) != 0 {
					t.Fatalf("expected no condition, got %+v", got)
				}
				return
			}
			if !found {
				t.Fatalf("expected reason %q, got %+v", tc.wantReason, got)
			}
			if sev != tc.wantSev {
				t.Errorf("reason %q: want severity %q, got %q", tc.wantReason, tc.wantSev, sev)
			}
		})
	}
}

func u64(v uint64) *uint64 { return &v }
