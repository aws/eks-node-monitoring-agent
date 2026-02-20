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
var WellKnownXidCodes = []uint{13, 31, 48, 63, 64, 74, 79, 94, 95, 119, 120, 121, 140}

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
