//go:build !darwin

package dcgm

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

// TestClassify_fabricHealthMaskSpec is a spec-derived table for the packed
// fabric health mask (DCGM field 174 / NVML_GPU_FABRIC_HEALTH_MASK). The mask
// values and expected verdicts come from the NVIDIA specification sub-field
// encoding (0=NotSupported, 1=True/fault, 2=False/healthy; incorrect_configuration
// value >=2 = fault), not from observed values, which drift across driver
// releases. The production watchfield delegates to Classify, so this exercises
// the real decode path. In particular it guards that a non-zero-but-healthy mask
// such as 0x80 classifies as healthy, so a "non-zero means fault" misread cannot
// regress.
func TestClassify_fabricHealthMaskSpec(t *testing.T) {
	cases := []struct {
		name        string
		mask        uint64
		expectFatal bool
		wantMessage string // if set, the exact expected message
	}{
		{"0x0 all sub-fields NotSupported", 0x0, false, ""},
		{"0x80 access_timeout_recovery=False, healthy (regression guard)", 0x80, false, ""},
		{"0x100 incorrect_configuration=1 (None / correct)", 0x100, false, ""},
		{"0x1 degraded_bw=True", 0x1, true, ""},
		{"0x4 route_recovery=True", 0x4, true, ""},
		{"0x10 route_unhealthy=True", 0x10, true, ""},
		{"0x40 access_timeout_recovery=True", 0x40, true, ""},
		{"0x200 incorrect_configuration=2 (incorrect)", 0x200, true, ""},
		{"0x11 degraded_bw + route_unhealthy (multi-fault)", 0x11, true, "GPU fabric health mask 0x11: degraded_bw=1, route_unhealthy=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mask := tc.mask
			got := Classify(NormalizedSignals{FabricHealthMask: &mask})
			sev, found := reasonSeverity(got, "NvidiaFabricError")

			if !tc.expectFatal {
				if len(got) != 0 {
					t.Fatalf("mask 0x%x: expected no condition (healthy), got %+v", tc.mask, got)
				}
				return
			}
			if !found {
				t.Fatalf("mask 0x%x: expected NvidiaFabricError, got %+v", tc.mask, got)
			}
			if sev != monitor.SeverityFatal {
				t.Errorf("mask 0x%x: want Fatal, got %q", tc.mask, sev)
			}
			// For a multi-fault mask the message must list every faulting
			// sub-field, in order, joined exactly as production emits it.
			if tc.wantMessage != "" {
				if msg := fabricConditionMessage(got); msg != tc.wantMessage {
					t.Errorf("mask 0x%x: message: want %q, got %q", tc.mask, tc.wantMessage, msg)
				}
			}
		})
	}
}

// fabricConditionMessage returns the message of the NvidiaFabricError condition,
// or "" if none is present.
func fabricConditionMessage(conds []monitor.Condition) string {
	for _, c := range conds {
		if c.Reason == "NvidiaFabricError" {
			return c.Message
		}
	}
	return ""
}
