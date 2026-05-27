//go:build !darwin

package dcgm

import (
	"context"
	"fmt"
	"slices"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// WellKnownXidCodes is a curated list of NVIDIA XID error codes that are known to indicate
// critical GPU failures requiring immediate attention. These codes are documented in NVIDIA's
// official XID error documentation: https://docs.nvidia.com/deploy/xid-errors/index.html
//
// The agent treats XID codes in this list as Fatal errors (setting node conditions), while
// unknown XID codes are treated as Warnings (logged as Kubernetes events). This classification
// is based on operational experience and NVIDIA's severity guidelines.
//
// Users can customize this behavior by modifying the list or implementing custom XID handling
// logic based on their specific GPU workload requirements and operational policies.
//
// Each code below includes its NVIDIA resolution bucket (immediate action) and the
// EKS-specific remediation. On EKS, the only automated remediation available is node
// replacement via the AcceleratedHardwareReady condition, which is appropriate for all
// codes in this list since they indicate hardware states that cannot be recovered without
// at minimum a GPU reset. Today this requires draining the node and replacing it, but
// in the future EKS may support in-place GPU resets without full node replacement.
//
// Resolution Bucket Reference (from NVIDIA XID Catalog):
//
//	RESET_GPU: Terminate all GPU processes and reset the GPU using "nvidia-smi -r".
//	  If the error persists after reset, a node reboot is required. If it persists
//	  after reboot, the hardware should be replaced.
//
//	WORKFLOW_XID_48: Reset the GPU. After reset, check SRAM DBE thresholds via
//	  nvidia-smi (sram_threshold_exceeded) or NSM Msg Type 0x3, Cmd Code 0x7D, bit 0.
//	  If the threshold flag is set, run field diagnostics. Persistent errors indicate
//	  hardware degradation requiring RMA.
//
//	WORKFLOW_NVLINK_ERR: Reset the GPU or reboot the node. Use "nvidia-smi nvlink" for
//	  additional details on link errors. If the error recurs after reset/reboot, the
//	  hardware should be replaced.
//
//	RESTART_BM: The GPU is no longer accessible over PCIe. A full power cycle
//	  (bare-metal restart) is required. This typically indicates PCIe link hardware failure.
//
//	CHECK_MECHANICALS: Verify physical connections to the GPU board. Typically indicates
//	  auxiliary power connectors are not properly seated.
//
//	RESTART_VM: Restart the virtual machine or instance. On EKS this maps to node
//	  replacement since instances cannot be restarted in place.
//
//	CONTACT_SUPPORT: No automated recovery is possible. The error requires investigation
//	  by the hardware vendor. On EKS this maps to node replacement since the GPU is in
//	  an unrecoverable state.
//
//	IGNORE: The event is informational per NVIDIA's guidance. However, for XID 63
//	  (memory remapping), EC2 recommends replacing the instance because repeated
//	  remapping events indicate degrading GPU memory that will eventually exhaust
//	  available remapping resources (escalating to XID 64). We include XID 63 in the
//	  well-known list to align with EC2's recommendation of proactive replacement.
//
// Well-Known XID Codes:
//
//   - 46: GPU stopped processing (RESET_GPU).
//   - 48: Double Bit ECC Error (WORKFLOW_XID_48).
//   - 54: Auxiliary power not connected (CHECK_MECHANICALS).
//   - 62: Internal micro-controller halt (RESET_GPU).
//   - 63: GPU memory remapping event (NVIDIA: IGNORE, EC2: replace instance).
//   - 64: GPU memory remapping failure (RESET_GPU).
//   - 74: NVLink Error (WORKFLOW_NVLINK_ERR).
//   - 79: GPU has fallen off the bus (RESTART_BM).
//   - 95: Uncontained memory error (RESET_GPU).
//   - 109: Context switch timeout (RESET_GPU).
//   - 110: Security fault error (RESET_GPU).
//   - 119: GSP RPC timeout (RESET_GPU).
//   - 120: GSP error (RESET_GPU).
//   - 136: Link training failed (RESET_GPU).
//   - 140: Unrecoverable ECC error (RESET_GPU).
//   - 142: NVENC3 error (CONTACT_SUPPORT). Applies to GB200. Video encoder hardware
//     failure with no automated recovery; on EKS the node must be replaced.
//   - 143: GPU initialization error (RESET_GPU).
//   - 151: Key rotation error (RESTART_VM). Applies to H100/B100/GB200. Confidential
//     computing key rotation failure; on EKS the instance must be replaced.
//   - 155: NVLink SW defined error (RESET_GPU).
//   - 156: Resource retirement event (RESET_GPU).
//   - 158: GPU fatal timeout (RESET_GPU).
//
// For investigatory steps when errors persist after the immediate action, see:
// https://docs.nvidia.com/deploy/xid-errors/analyzing-xid-catalog.html
// https://docs.nvidia.com/deploy/gpu-debug-guidelines/index.html
var WellKnownXidCodes = []uint{46, 48, 54, 62, 63, 64, 74, 79, 95, 109, 110, 119, 120, 136, 140, 142, 143, 151, 155, 156, 158}

func (s *DCGMSystem) Policies(ctx context.Context) ([]monitor.Condition, error) {
	condition := s.handle(ctx)
	if condition != nil {
		return []monitor.Condition{*condition}, nil
	}
	return nil, nil
}

func (s *DCGMSystem) handle(ctx context.Context) *monitor.Condition {
	logger := log.FromContext(ctx)
	logger.V(4).Info("waiting for next DCGM policy violation finding")
	// policy events are stores in a 'Data' field as an interface{}, so once we
	// know the type we can cast it to the expected struct.
	switch finding := <-s.dcgm.PolicyViolationChannel(); finding.Condition {
	case dcgmapi.XidPolicy:
		return s.handleXidFinding(finding.Data.(dcgmapi.XidPolicyCondition))
	case dcgmapi.DbePolicy:
		return s.handleDbeFinding(finding.Data.(dcgmapi.DbePolicyCondition))
	case dcgmapi.NvlinkPolicy:
		return s.handleNvlinkFinding(finding.Data.(dcgmapi.NvlinkPolicyCondition))
	case dcgmapi.MaxRtPgPolicy:
		return s.handlePageRetirementFinding(finding.Data.(dcgmapi.RetiredPagesPolicyCondition))
	case dcgmapi.PowerPolicy:
		return s.handlePowerFinding(finding.Data.(dcgmapi.PowerPolicyCondition))
	case dcgmapi.PCIePolicy:
		return s.handlePCIePolicyFinding(finding.Data.(dcgmapi.PciPolicyCondition))
	case dcgmapi.ThermalPolicy:
		return s.handleThermalPolicyFinding(finding.Data.(dcgmapi.ThermalPolicyCondition))
	default:
		logger.V(4).Info("unhandled policy violation finding", "finding", finding)
		return nil
	}
}

func (s *DCGMSystem) handleXidFinding(xidFinding dcgmapi.XidPolicyCondition) *monitor.Condition {
	xidCode := xidFinding.ErrNum
	if slices.Contains(WellKnownXidCodes, xidCode) {
		condition := reasons.NvidiaXIDError.
			Builder(xidCode).
			Message(fmt.Sprintf("detected XID-%d on the instance, review kernel logs for additional information.", xidCode)).
			Build()
		return &condition
	} else {
		// If the XID code is not well known, emit a warning (i.e Kubernetes event) rather than set a node condition.
		condition := reasons.NvidiaXIDWarning.
			Builder(xidCode).
			Message(fmt.Sprintf("detected unknown XID-%d on the instance, review kernel logs for additional information.", xidCode)).
			Build()
		return &condition
	}
}

func (s *DCGMSystem) handleDbeFinding(dbeData dcgmapi.DbePolicyCondition) *monitor.Condition {
	condition := reasons.NvidiaDoubleBitError.
		Builder().
		Message(fmt.Sprintf("detected %d Nvidia Double Bit error(s) on location %v", dbeData.NumErrors, dbeData.Location)).
		Build()
	return &condition
}

func (s *DCGMSystem) handleNvlinkFinding(nvLinkData dcgmapi.NvlinkPolicyCondition) *monitor.Condition {
	condition := reasons.NvidiaNVLinkError.
		Builder().
		Message(fmt.Sprintf("detected %d NVLink errors on fieldId %v", nvLinkData.Counter, nvLinkData.FieldId)).
		Build()
	return &condition
}

func (s *DCGMSystem) handlePageRetirementFinding(retirementData dcgmapi.RetiredPagesPolicyCondition) *monitor.Condition {
	condition := reasons.NvidiaPageRetirement.
		Builder().
		Message(fmt.Sprintf("detected %d SBE, and %d DBE page retirements", retirementData.SbePages, retirementData.DbePages)).
		Build()
	return &condition
}

func (s *DCGMSystem) handlePowerFinding(powerData dcgmapi.PowerPolicyCondition) *monitor.Condition {
	// see: https://github.com/NVIDIA/DCGM/blob/d47c0b77920f8dbfef588eaac2cbbea3401ef463/dcgmlib/dcgm_errors.h#L162-L171
	if powerData.PowerViolation != 0 {
		condition := reasons.NvidiaPowerError.
			Builder().
			Message(fmt.Sprintf("detected power usage outside of thresholds with severity code %d", powerData.PowerViolation)).
			Build()
		return &condition
	}
	return nil
}

func (s *DCGMSystem) handlePCIePolicyFinding(pcieData dcgmapi.PciPolicyCondition) *monitor.Condition {
	condition := reasons.NvidiaPCIeError.
		Builder().
		Message(fmt.Sprintf("detected %d PCIe replays", pcieData.ReplayCounter)).
		Build()
	return &condition
}

func (s *DCGMSystem) handleThermalPolicyFinding(thermalData dcgmapi.ThermalPolicyCondition) *monitor.Condition {
	// see: https://github.com/NVIDIA/DCGM/blob/d47c0b77920f8dbfef588eaac2cbbea3401ef463/dcgmlib/dcgm_errors.h#L162-L171
	if thermalData.ThermalViolation != 0 {
		condition := reasons.NvidiaThermalError.
			Builder().
			Message(fmt.Sprintf("detected GPU thermals outside of thresholds with severity code %d", thermalData.ThermalViolation)).
			Build()
		return &condition
	}
	return nil
}
