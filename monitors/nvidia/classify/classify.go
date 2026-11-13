// Package classify maps GPU health signals to NMA node conditions. It holds
// the classification policy for the nvidia monitor and has no dependency on
// any signal source, so a DCGM reader and an NVML reader share it.
package classify

import (
	"fmt"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// NormalizedSignals is a source-agnostic snapshot of GPU health signals.
//
// It is the seam between signal *acquisition* and signal *classification*.
// Today package dcgm (dcgm_policies.go, dcgm_watchfield.go, dcgm_devices.go)
// is the only producer, but additional signal sources may populate it.
// Because Classify is a pure function over this struct, the classification
// policy and its unit tests bind to any source unchanged, provided the source
// fills each field in its documented encoding. Some fields use a DCGM-specific
// encoding: FabricManagerStatus is a dcgmFabricManagerStatus_t, so a non-DCGM
// source (e.g. NVML, which reports fabric state and status separately) must
// translate its values to that enum, and the resulting messages name DCGM's
// statuses. FabricHealthMask and FabricManagerStatus may also hold DCGM's blank
// values (see isInt64Blank), which mean DCGM could not read the field;
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

	FabricCrossCheck *FabricCrossCheck // collected only for a faulting FabricHealthMask; nil when not collected
}

// FabricCrossCheck is nvidia-smi's view of the GPU fabric. nvidia-smi decodes
// the fabric health mask itself, so it can confirm or contradict a mask fault.
type FabricCrossCheck struct {
	GPUs             []GPUFabricHealth // one entry per GPU that nvidia-smi lists
	ProcessesRunning bool              // nvidia-smi listed a process on at least one GPU
}

// GPUFabricHealth is one GPU's fabric health as nvidia-smi reports it.
type GPUFabricHealth struct {
	BusID         string // PCI bus ID, e.g. 00000000:51:00.0
	HealthSummary string // <fabric><health><summary>: Healthy, Unhealthy, Limited Capacity, or N/A
}

// Classify maps a source-agnostic signal snapshot to node conditions. It is
// pure (no I/O, no DCGM calls). Severity comes from pkg/reasons:
// Fatal => AcceleratedHardwareReady=False => repair-eligible; Warning => event only.
func Classify(s NormalizedSignals) []monitor.Condition {
	var out []monitor.Condition

	// XID: well-known => Fatal, otherwise Warning (see WellKnownXidCodes).
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
	if s.FabricHealthMask != nil && !isInt64Blank(int64(*s.FabricHealthMask)) {
		if faults := fabricHealthMaskFaults(int64(*s.FabricHealthMask)); len(faults) > 0 {
			message := fmt.Sprintf("GPU fabric health mask 0x%x: %s", *s.FabricHealthMask, strings.Join(faults, ", "))
			out = append(out, fabricHealthMaskFault(message, s.FabricCrossCheck, s.GPUCount))
		}
	}

	// Fabric Manager status. A blank value means DCGM could not read the
	// status, so there is nothing to classify.
	if s.FabricManagerStatus != nil && !isInt64Blank(*s.FabricManagerStatus) {
		switch status := *s.FabricManagerStatus; status {
		case DcgmFMStatusSuccess, DcgmFMStatusNotSupported, DcgmFMStatusInProgress, DcgmFMStatusNotStarted:
			// Healthy or not applicable: no condition.
			// NotSupported: FM not applicable (e.g. single-GPU, or rack-level
			// NVSwitch like GB200/GB300).
			// NotStarted: FM hasn't started yet; skipped to avoid false
			// positives on instances where FM is not needed but DCGM doesn't
			// report NotSupported (e.g. GB200/GB300).
			// InProgress: FM is still performing fabric training during boot.
			// Success: FM is running and healthy.
		case DcgmFMStatusUnrecognized, DcgmFMStatusNvmlTooOld, dcgmStatusBadParam:
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

	// Device count: the reader's GPU count and the host's device files must
	// agree, and the reader must see at least as many GPUs as the instance type
	// provides. The second check catches a GPU that failed to enumerate on the
	// PCIe bus at boot, where both counts agree on the wrong, lower number.
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

// fabricHealthMaskFault is Fatal unless nvidia-smi lists as many GPUs as DCGM
// and reports every one Healthy, in which case it is a Warning. All GPUs must
// be Healthy because DCGM GPU IDs do not map to nvidia-smi bus IDs. Running
// processes never decide the severity: a busy GPU can still be faulty.
func fabricHealthMaskFault(message string, check *FabricCrossCheck, gpuCount *uint) monitor.Condition {
	if check == nil {
		return reasons.NvidiaFabricError.Builder().Message(message).Build()
	}
	if gpuCount == nil || uint(len(check.GPUs)) != *gpuCount {
		dcgmCount := "an unknown number of"
		if gpuCount != nil {
			dcgmCount = fmt.Sprint(*gpuCount)
		}
		return reasons.NvidiaFabricError.Builder().
			Message(fmt.Sprintf("%s; nvidia-smi lists %d GPUs but DCGM detected %s GPUs", message, len(check.GPUs), dcgmCount)).
			Build()
	}
	for _, gpu := range check.GPUs {
		if gpu.HealthSummary != "Healthy" {
			return reasons.NvidiaFabricError.Builder().
				Message(fmt.Sprintf("%s; nvidia-smi fabric health summary for GPU %s: %q", message, gpu.BusID, gpu.HealthSummary)).
				Build()
		}
	}
	running := "no"
	if check.ProcessesRunning {
		running = "yes"
	}
	return reasons.NvidiaFabricWarning.Builder().
		Message(fmt.Sprintf("%s; nvidia-smi reports fabric health summary Healthy on all %d GPUs; GPU processes running: %s", message, len(check.GPUs), running)).
		Build()
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
