//go:build !darwin

package dcgm

import (
	"fmt"
	"strings"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// NormalizedSignals is a source-agnostic snapshot of GPU health signals.
//
// It is the seam between signal *acquisition* and signal *classification*.
// Today the DCGM reader (dcgm_policies.go / dcgm_watchfield.go / dcgm_devices.go) is the only
// producer, but additional signal sources may populate it in the future.
// Because Classify is a pure function over this struct, the classification
// policy and its unit tests bind to any source unchanged, provided the source
// fills each field in its documented encoding. Some fields use a DCGM-specific
// encoding: FabricManagerStatus is a dcgmFabricManagerStatus_t, so a non-DCGM
// source (e.g. NVML, which reports fabric state and status separately) must
// translate its values to that enum, and the resulting messages name DCGM's
// statuses. FabricHealthMask and FabricManagerStatus may also hold DCGM's blank
// values (dcgmapi.IsInt64Blank), which mean DCGM could not read the field;
// Classify treats them as not observed.
type NormalizedSignals struct {
	XIDs                []uint  // observed XID error codes (DCGM XidPolicy today)
	FabricHealthMask    *uint64 // NVLink fabric health mask; nil when not applicable/observed
	FabricManagerStatus *int64  // dcgmFabricManagerStatus_t (DCGM_FI_DEV_FABRIC_MANAGER_STATUS); nil when not observed
	DoubleBitECC        bool    // DCGM DbePolicy
	NVLinkError         bool    // DCGM NvlinkPolicy (hard NVLink error)
	PageRetirement      bool    // DCGM MaxRtPgPolicy
	PowerViolation      bool    // DCGM PowerPolicy
	ThermalViolation    bool    // DCGM ThermalPolicy
	PCIeReplay          bool    // DCGM PCIePolicy
	GPUCount            *uint   // GPUs visible to DCGM; nil when not observed
	GPUDeviceFileCount  *uint   // /dev/nvidia<N> device files on the host; nil when not observed
	GPUDeviceFilePath   string  // directory the device files were counted in (/dev or the GPU Operator driver root)
	ExpectedGPUCount    *uint   // GPUs expected for the EC2 instance type; nil when unknown
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
	// driver-version-aware place that tests can pin. A blank value means DCGM
	// could not read the mask, so there is nothing to decode.
	if s.FabricHealthMask != nil && !dcgmapi.IsInt64Blank(int64(*s.FabricHealthMask)) {
		if faults := fabricHealthMaskFaults(int64(*s.FabricHealthMask)); len(faults) > 0 {
			out = append(out, reasons.NvidiaFabricError.Builder().
				Message(fmt.Sprintf("GPU fabric health mask 0x%x: %s", *s.FabricHealthMask, strings.Join(faults, ", "))).
				Build())
		}
	}

	// Fabric Manager status: see handleFabricField (dcgm_watchfield.go) for why
	// NotSupported, NotStarted, InProgress, and Success are treated as healthy.
	// A blank value means DCGM could not read the status, so there is nothing to
	// classify.
	if s.FabricManagerStatus != nil && !dcgmapi.IsInt64Blank(*s.FabricManagerStatus) {
		switch status := *s.FabricManagerStatus; status {
		case DcgmFMStatusSuccess, DcgmFMStatusNotSupported, DcgmFMStatusInProgress, DcgmFMStatusNotStarted:
			// Healthy or not applicable: no condition.
		case DcgmFMStatusUnrecognized, DcgmFMStatusNvmlTooOld, dcgmapi.DCGM_ST_BADPARAM:
			// The driver/NVML and DCGM versions do not match, so the status is
			// unknown rather than failed: NvmlTooOld means NVML has no fabric
			// API, and DCGM stores DCGM_ST_BADPARAM (-1) instead of Unrecognized
			// when NVML reports a fabric state DCGM does not know. A replacement
			// node with the same AMI would report the same, so this is a Warning.
			out = append(out, reasons.FabricManagerNotRunningWarning.Builder().
				Message(fabricManagerStatusMessage(status)).
				Build())
		default:
			out = append(out, reasons.FabricManagerNotRunning.Builder().
				Message(fabricManagerStatusMessage(status)).
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

	// Device count: DCGM and the host's device files must agree, and DCGM must
	// see at least as many GPUs as the instance type provides (see DeviceCount,
	// dcgm_devices.go, for why both checks are needed).
	if s.GPUCount != nil && s.GPUDeviceFileCount != nil && *s.GPUCount != *s.GPUDeviceFileCount {
		out = append(out, reasons.NvidiaDeviceCountMismatch.Builder().
			Message(fmt.Sprintf("DCGM detected %d GPUs but %d nvidia device files were detected at %s", *s.GPUCount, *s.GPUDeviceFileCount, s.GPUDeviceFilePath)).
			Build())
	}
	if s.GPUCount != nil && s.ExpectedGPUCount != nil && *s.GPUCount < *s.ExpectedGPUCount {
		out = append(out, reasons.NvidiaDeviceCountMismatch.Builder().
			Message(fmt.Sprintf("expected %d GPUs for this instance type but only %d were detected — possible hardware failure",
				*s.ExpectedGPUCount, *s.GPUCount)).
			Build())
	}

	return out
}

// fabricManagerStatusMessage names a dcgmFabricManagerStatus_t value, or
// Unknown(<n>) for a value outside the enum.
func fabricManagerStatusMessage(status int64) string {
	name := fabricManagerStatusNames[status]
	if name == "" {
		name = fmt.Sprintf("Unknown(%d)", status)
	}
	return fmt.Sprintf("Fabric Manager status: %s", name)
}

func isWellKnownXid(xid uint) bool {
	for _, v := range WellKnownXidCodes {
		if v == xid {
			return true
		}
	}
	return false
}
