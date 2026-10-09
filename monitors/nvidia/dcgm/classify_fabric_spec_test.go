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
		// DCGM's blank values mean the mask could not be read. Decoded as a mask
		// they would show faults (e.g. incorrect_configuration=15), so they must
		// be skipped rather than decoded.
		{"blank", 0x7ffffffffffffff0, false, ""},                // DCGM_FT_INT64_BLANK
		{"blank: not found", 0x7ffffffffffffff1, false, ""},     // DCGM_FT_INT64_NOT_FOUND
		{"blank: not supported", 0x7ffffffffffffff2, false, ""}, // DCGM_FT_INT64_NOT_SUPPORTED
		{"blank: no permission", 0x7ffffffffffffff3, false, ""}, // DCGM_FT_INT64_NOT_PERMISSIONED
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

// TestClassify_fabricManagerStatusSpec covers every dcgmFabricManagerStatus_t
// value (dcgm_structs.h), the values DCGM stores outside that enum, and
// out-of-range values. NotSupported, NotStarted, InProgress, and Success are
// healthy (see handleFabricField for why NotStarted is suppressed), and DCGM's
// blank values (the status could not be read) produce no condition.
// Unrecognized, NvmlTooOld, and -1 (DCGM_ST_BADPARAM, which DCGM stores for a
// fabric state it does not know) mean the driver/NVML and DCGM versions do not
// match and are a Warning. Anything else is a Fatal FabricManagerNotRunning
// naming the status, or Unknown(<n>) for a value outside the enum.
func TestClassify_fabricManagerStatusSpec(t *testing.T) {
	cases := []struct {
		name        string
		status      int64
		wantMessage string // "" => healthy, no condition
		wantSev     monitor.Severity
	}{
		{"0 NotSupported", 0, "", ""}, // DcgmFMStatusNotSupported
		{"1 NotStarted", 1, "", ""},   // DcgmFMStatusNotStarted
		{"2 InProgress", 2, "", ""},   // DcgmFMStatusInProgress
		{"3 Success", 3, "", ""},      // DcgmFMStatusSuccess
		{"4 Failure", 4, "Fabric Manager status: Failure", monitor.SeverityFatal},                      // DcgmFMStatusFailure
		{"5 Unrecognized", 5, "Fabric Manager status: Unrecognized", monitor.SeverityWarning},          // DcgmFMStatusUnrecognized
		{"6 NvmlTooOld", 6, "Fabric Manager status: NvmlTooOld", monitor.SeverityWarning},              // DcgmFMStatusNvmlTooOld
		{"7 out of range", 7, "Fabric Manager status: Unknown(7)", monitor.SeverityFatal},              // DcgmFMStatusCount: the enum size, not a status
		{"-1 unknown fabric state", -1, "Fabric Manager status: Unknown(-1)", monitor.SeverityWarning}, // dcgmapi.DCGM_ST_BADPARAM
		{"-2 out of range", -2, "Fabric Manager status: Unknown(-2)", monitor.SeverityFatal},
		{"blank", 0x7ffffffffffffff0, "", ""},                // DCGM_FT_INT64_BLANK
		{"blank: not found", 0x7ffffffffffffff1, "", ""},     // DCGM_FT_INT64_NOT_FOUND
		{"blank: not supported", 0x7ffffffffffffff2, "", ""}, // DCGM_FT_INT64_NOT_SUPPORTED
		{"blank: no permission", 0x7ffffffffffffff3, "", ""}, // DCGM_FT_INT64_NOT_PERMISSIONED
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			got := Classify(NormalizedSignals{FabricManagerStatus: &status})
			if tc.wantMessage == "" {
				if len(got) != 0 {
					t.Fatalf("status %d: expected no condition (healthy), got %+v", tc.status, got)
				}
				return
			}
			want := []monitor.Condition{{
				Reason:   "FabricManagerNotRunning",
				Message:  tc.wantMessage,
				Severity: tc.wantSev,
			}}
			if len(got) != 1 || got[0].Reason != want[0].Reason || got[0].Message != want[0].Message || got[0].Severity != want[0].Severity {
				t.Fatalf("status %d: want %+v, got %+v", tc.status, want, got)
			}
		})
	}
}
