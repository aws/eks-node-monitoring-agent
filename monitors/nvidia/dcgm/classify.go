//go:build !darwin

package dcgm

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// NormalizedSignals is a source-agnostic snapshot of GPU health signals.
//
// It is the seam between signal *acquisition* and signal *classification*.
// Today the DCGM reader (dcgm_policies.go / dcgm_watchfield.go) is the only
// producer, but additional signal sources may populate it in the future.
// Because Classify is a pure function over this struct, the classification
// policy and its unit tests bind to any source unchanged.
//
// Fields are grouped by whether NMA populates them today. The "not read today"
// group is deliberately present so its Classify rules and tests exist now: a
// source that starts populating one of those fields makes the corresponding
// (currently test-only) rule fire in production.
type NormalizedSignals struct {
	// ---- Populated by the current DCGM reader ----
	XIDs             []uint  // observed XID error codes (DCGM XidPolicy today)
	FabricHealthMask *uint64 // NVLink fabric health mask; nil when not applicable/observed
	DriverVersion    string  // fabric-mask semantics are driver-version dependent
	DoubleBitECC     bool    // DCGM DbePolicy
	NVLinkError      bool    // DCGM NvlinkPolicy (hard NVLink error)
	PageRetirement   bool    // DCGM MaxRtPgPolicy
	PowerViolation   bool    // DCGM PowerPolicy
	ThermalViolation bool    // DCGM ThermalPolicy
	PCIeReplay       bool    // DCGM PCIePolicy
	ExpectedGPUCount int     // per-generation expected device count (0 = unknown)
	ActualGPUCount   int

	// ---- NOT read by NMA today: a future signal source may populate these.
	// Rules below exist so the tests define the intended behavior; until a
	// source fills the field, only tests exercise the rule (which is exactly
	// the false negative reproduced in the tests). ----
	NVLinkEffectiveBER *float64 // effective-BER threshold breach (diag err 119); a degrading link NMA does not catch today
	GflopsBelowTol     bool     // throughput below tolerance (diag err 110)  [TODO: dedicated reason]
	DCGMDiagErrorIDs   []int    // other active-diag error_ids not covered above (86/87 broken-p2p, 58 mem-mismatch, ...) [TODO]
}

// Classify maps a source-agnostic signal snapshot to node conditions. It is
// pure (no I/O, no DCGM calls). Severity comes from pkg/reasons:
// Fatal => AcceleratedHardwareReady=False => repair-eligible; Warning => event only.
func Classify(s NormalizedSignals) []monitor.Condition {
	var out []monitor.Condition

	// XID: well-known => Fatal, otherwise Warning. Mirrors handleXidFinding so
	// this can replace it once the DCGM reader emits NormalizedSignals.
	for _, xid := range s.XIDs {
		if isWellKnownXid(xid) {
			out = append(out, reasons.NvidiaXIDError.Builder(xid).
				Message(fmt.Sprintf("detected XID-%d on the instance, review kernel logs for additional information.", xid)).
				Build())
		} else {
			out = append(out, reasons.NvidiaXIDWarning.Builder(xid).
				Message(fmt.Sprintf("detected unknown XID-%d on the instance, review kernel logs for additional information.", xid)).
				Build())
		}
	}

	// Fabric health mask: decoded via the shared fabricHealthMaskFaults.
	// Reusing it keeps the 0x80-is-healthy semantics in one spec-backed,
	// driver-version-aware place that tests can pin.
	if s.FabricHealthMask != nil {
		if faults := fabricHealthMaskFaults(int64(*s.FabricHealthMask)); len(faults) > 0 {
			out = append(out, reasons.NvidiaFabricError.Builder().
				Message(fmt.Sprintf("fabric health mask 0x%x indicates fault(s) %v (driver %s)", *s.FabricHealthMask, faults, s.DriverVersion)).
				Build())
		}
	}

	if s.DoubleBitECC {
		out = append(out, reasons.NvidiaDoubleBitError.Builder().Message("detected NVIDIA double-bit ECC error").Build())
	}
	if s.NVLinkError {
		out = append(out, reasons.NvidiaNVLinkError.Builder().Message("detected NVIDIA NVLink error").Build())
	}
	if s.ExpectedGPUCount > 0 && s.ActualGPUCount < s.ExpectedGPUCount {
		out = append(out, reasons.NvidiaDeviceCountMismatch.Builder().
			Message(fmt.Sprintf("expected %d GPUs for this instance type but detected %d", s.ExpectedGPUCount, s.ActualGPUCount)).Build())
	}
	if s.PageRetirement {
		out = append(out, reasons.NvidiaPageRetirement.Builder().Message("page retirement threshold reached").Build())
	}
	if s.PowerViolation {
		out = append(out, reasons.NvidiaPowerError.Builder().Message("GPU power violation").Build())
	}
	if s.ThermalViolation {
		out = append(out, reasons.NvidiaThermalError.Builder().Message("GPU thermal violation").Build())
	}
	if s.PCIeReplay {
		out = append(out, reasons.NvidiaPCIeError.Builder().Message("GPU PCIe replay").Build())
	}

	// ---- Signals not read today (rules defined ahead of a source) ----
	// NVLink effective BER: a degrading link can breach the effective-BER
	// threshold (diag err 119) while passing XID/ECC; classify it Fatal.
	// Today nothing populates this, so the rule never fires in production
	// -> the false negative. A future source must populate it.
	if s.NVLinkEffectiveBER != nil {
		out = append(out, reasons.NvidiaNVLinkError.Builder().
			Message(fmt.Sprintf("NVLink effective BER %.2e exceeds threshold (err 119)", *s.NVLinkEffectiveBER)).
			Build())
	}
	// TODO: GflopsBelowTol (err 110) and DCGMDiagErrorIDs (86/87/58/...) need
	// dedicated reasons before they can be classified; carried here so the
	// signal is captured and the follow-up is explicit.

	return out
}

func isWellKnownXid(xid uint) bool {
	for _, v := range WellKnownXidCodes {
		if v == xid {
			return true
		}
	}
	return false
}
