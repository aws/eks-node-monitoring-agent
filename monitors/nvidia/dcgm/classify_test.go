//go:build !darwin

package dcgm

import (
	"fmt"
	"slices"
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

// TestClassify_xidAllowlist locks the well-known XID allowlist. The expected
// codes are an explicit literal rather than a loop over WellKnownXidCodes, so an
// accidental addition to or removal from the allowlist fails here and shows up
// in review. Every listed code must classify as a Fatal NvidiaXID<code>Error
// (which sets the node condition); any other code must classify as a Warning
// NvidiaXID<code>Warning (a Kubernetes event only).
func TestClassify_xidAllowlist(t *testing.T) {
	wellKnown := []uint{46, 48, 54, 62, 63, 64, 74, 79, 95, 109, 110, 119, 120, 136, 140, 142, 143, 151, 155, 156, 158}

	sorted := slices.Clone(WellKnownXidCodes)
	slices.Sort(sorted)
	if !slices.Equal(sorted, wellKnown) {
		t.Fatalf("WellKnownXidCodes changed:\n got  %v\n want %v\nupdate this test deliberately if the change is intended", sorted, wellKnown)
	}

	for _, xid := range wellKnown {
		t.Run(fmt.Sprintf("well-known XID %d => Fatal", xid), func(t *testing.T) {
			assertSingleCondition(t, Classify(NormalizedSignals{XIDs: []uint{xid}}),
				fmt.Sprintf("NvidiaXID%dError", xid), monitor.SeverityFatal)
		})
	}

	// Codes outside the allowlist, including ones NVIDIA classifies as
	// application-level or informational (e.g. 13, 31, 43, 45, 94, 121).
	for _, xid := range []uint{0, 13, 31, 43, 45, 94, 121, 1000} {
		t.Run(fmt.Sprintf("unlisted XID %d => Warning", xid), func(t *testing.T) {
			assertSingleCondition(t, Classify(NormalizedSignals{XIDs: []uint{xid}}),
				fmt.Sprintf("NvidiaXID%dWarning", xid), monitor.SeverityWarning)
		})
	}
}

func assertSingleCondition(t *testing.T, got []monitor.Condition, wantReason string, wantSev monitor.Severity) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want exactly one condition, got %+v", got)
	}
	if got[0].Reason != wantReason || got[0].Severity != wantSev {
		t.Errorf("want %s/%s, got %s/%s", wantReason, wantSev, got[0].Reason, got[0].Severity)
	}
}

// TestClassify_deviceCount covers both device-count checks: DCGM's GPU count
// must match the host's /dev/nvidia<N> device files, and must be at least the
// instance type's expected count when that is known. Each mismatch is its own
// Fatal NvidiaDeviceCountMismatch condition with an exact message.
func TestClassify_deviceCount(t *testing.T) {
	u := func(v uint) *uint { return &v }
	const (
		fileMismatch  = "DCGM detected %d GPUs but %d nvidia device files were detected at /dev"
		belowExpected = "expected %d GPUs for this instance type but only %d were detected — possible hardware failure"
	)
	for _, tc := range []struct {
		name     string
		in       NormalizedSignals
		wantMsgs []string
	}{
		{"all agree", NormalizedSignals{GPUCount: u(4), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(4), ExpectedGPUCount: u(4)}, nil},
		{"expected unknown, counts agree", NormalizedSignals{GPUCount: u(4), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(4)}, nil},
		{"DCGM sees fewer than device files", NormalizedSignals{GPUCount: u(3), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(4)}, []string{fmt.Sprintf(fileMismatch, 3, 4)}},
		{"device files under the GPU Operator driver root", NormalizedSignals{GPUCount: u(3), GPUDeviceFilePath: "/run/nvidia/driver/dev", GPUDeviceFileCount: u(4)},
			[]string{"DCGM detected 3 GPUs but 4 nvidia device files were detected at /run/nvidia/driver/dev"}},
		{"DCGM sees more than device files", NormalizedSignals{GPUCount: u(5), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(4)}, []string{fmt.Sprintf(fileMismatch, 5, 4)}},
		{"both agree but below expected", NormalizedSignals{GPUCount: u(3), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(3), ExpectedGPUCount: u(4)}, []string{fmt.Sprintf(belowExpected, 4, 3)}},
		{"above expected is not flagged", NormalizedSignals{GPUCount: u(5), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(5), ExpectedGPUCount: u(4)}, nil},
		{"none detected", NormalizedSignals{GPUCount: u(0), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(0), ExpectedGPUCount: u(4)}, []string{fmt.Sprintf(belowExpected, 4, 0)}},
		{"both checks fire, in order", NormalizedSignals{GPUCount: u(3), GPUDeviceFilePath: "/dev", GPUDeviceFileCount: u(4), ExpectedGPUCount: u(4)},
			[]string{fmt.Sprintf(fileMismatch, 3, 4), fmt.Sprintf(belowExpected, 4, 3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want []monitor.Condition
			for _, m := range tc.wantMsgs {
				want = append(want, monitor.Condition{Reason: "NvidiaDeviceCountMismatch", Message: m, Severity: monitor.SeverityFatal})
			}
			got := Classify(tc.in)
			if len(got) != len(want) {
				t.Fatalf("want %d conditions %+v, got %+v", len(want), want, got)
			}
			for i := range want {
				if got[i].Reason != want[i].Reason || got[i].Message != want[i].Message || got[i].Severity != want[i].Severity {
					t.Errorf("condition %d: want %+v, got %+v", i, want[i], got[i])
				}
			}
		})
	}
}
