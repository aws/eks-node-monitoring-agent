//go:build !darwin

package dcgm

import (
	"fmt"
	"strings"

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
type NormalizedSignals struct {
	XIDs             []uint  // observed XID error codes (DCGM XidPolicy today)
	FabricHealthMask *uint64 // NVLink fabric health mask; nil when not applicable/observed
	DoubleBitECC     bool    // DCGM DbePolicy
	NVLinkError      bool    // DCGM NvlinkPolicy (hard NVLink error)
	PageRetirement   bool    // DCGM MaxRtPgPolicy
	PowerViolation   bool    // DCGM PowerPolicy
	ThermalViolation bool    // DCGM ThermalPolicy
	PCIeReplay       bool    // DCGM PCIePolicy
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
				Message(fmt.Sprintf("GPU fabric health mask 0x%x: %s", *s.FabricHealthMask, strings.Join(faults, ", "))).
				Build())
		}
	}

	if s.DoubleBitECC {
		out = append(out, reasons.NvidiaDoubleBitError.Builder().Message("detected NVIDIA double-bit ECC error").Build())
	}
	if s.NVLinkError {
		out = append(out, reasons.NvidiaNVLinkError.Builder().Message("detected NVIDIA NVLink error").Build())
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
