//go:build !darwin

package dcgm

import (
	"context"
	"fmt"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
)

func (s *DCGMSystem) Policies(ctx context.Context) ([]monitor.Condition, error) {
	return s.handle(ctx), nil
}

func (s *DCGMSystem) handle(ctx context.Context) []monitor.Condition {
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

// withMessage replaces the message of each classified condition with the
// source-specific message. Classify owns the decision (whether a condition is
// emitted, its reason, and its severity); the acquisition path owns the
// message, since it carries source-specific detail (counters, locations) that a
// source-agnostic classifier does not. Each handler classifies a single signal,
// so it yields at most one condition; none (a power/thermal violation of 0)
// yields an empty result.
func withMessage(conds []monitor.Condition, msg string) []monitor.Condition {
	for i := range conds {
		conds[i].Message = msg
	}
	return conds
}

// The handlers below delegate the reason+severity decision to the pure
// classify.Classify and plug in the same message text they emitted before,
// so both the repair behavior and the event/condition messages are unchanged.

func (s *DCGMSystem) handleXidFinding(xidFinding dcgmapi.XidPolicyCondition) []monitor.Condition {
	// Classify's XID messages are identical to the ones previously built here.
	return classify.Classify(classify.NormalizedSignals{XIDs: []uint{xidFinding.ErrNum}})
}

func (s *DCGMSystem) handleDbeFinding(dbeData dcgmapi.DbePolicyCondition) []monitor.Condition {
	return withMessage(classify.Classify(classify.NormalizedSignals{DoubleBitECC: true}),
		fmt.Sprintf("detected %d Nvidia Double Bit error(s) on location %v", dbeData.NumErrors, dbeData.Location))
}

func (s *DCGMSystem) handleNvlinkFinding(nvLinkData dcgmapi.NvlinkPolicyCondition) []monitor.Condition {
	return withMessage(classify.Classify(classify.NormalizedSignals{NVLinkError: true}),
		fmt.Sprintf("detected %d NVLink errors on fieldId %v", nvLinkData.Counter, nvLinkData.FieldId))
}

func (s *DCGMSystem) handlePageRetirementFinding(retirementData dcgmapi.RetiredPagesPolicyCondition) []monitor.Condition {
	return withMessage(classify.Classify(classify.NormalizedSignals{PageRetirement: true}),
		fmt.Sprintf("detected %d SBE, and %d DBE page retirements", retirementData.SbePages, retirementData.DbePages))
}

func (s *DCGMSystem) handlePowerFinding(powerData dcgmapi.PowerPolicyCondition) []monitor.Condition {
	// see: https://github.com/NVIDIA/DCGM/blob/d47c0b77920f8dbfef588eaac2cbbea3401ef463/dcgmlib/dcgm_errors.h#L162-L171
	return withMessage(classify.Classify(classify.NormalizedSignals{PowerViolation: powerData.PowerViolation != 0}),
		fmt.Sprintf("detected power usage outside of thresholds with severity code %d", powerData.PowerViolation))
}

func (s *DCGMSystem) handlePCIePolicyFinding(pcieData dcgmapi.PciPolicyCondition) []monitor.Condition {
	return withMessage(classify.Classify(classify.NormalizedSignals{PCIeReplay: true}),
		fmt.Sprintf("detected %d PCIe replays", pcieData.ReplayCounter))
}

func (s *DCGMSystem) handleThermalPolicyFinding(thermalData dcgmapi.ThermalPolicyCondition) []monitor.Condition {
	// see: https://github.com/NVIDIA/DCGM/blob/d47c0b77920f8dbfef588eaac2cbbea3401ef463/dcgmlib/dcgm_errors.h#L162-L171
	return withMessage(classify.Classify(classify.NormalizedSignals{ThermalViolation: thermalData.ThermalViolation != 0}),
		fmt.Sprintf("detected GPU thermals outside of thresholds with severity code %d", thermalData.ThermalViolation))
}
