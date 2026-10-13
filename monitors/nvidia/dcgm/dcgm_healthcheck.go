//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// nvlinkLifetimeCounterPrefixes lists the DCGM message prefixes emitted by
// MonitorNVLinkErrorFields when it fails on a non-zero lifetime-absolute NVLink
// counter (DCGM_FI_DEV_NVLINK_{CRC,RECOVERY,REPLAY}_ERROR_TOTAL, fields
// 497/498/499). These messages are distinct from the rate-delta (legacy) and
// Blackwell recovery-event paths that also emit DCGM_FR_NVLINK_ERROR_CRITICAL
// (code 71) — those paths do indicate active faults and must remain Fatal.
//
// The strings are stable because DCGM_VERSION is pinned in our Dockerfile; a
// unit test re-checks them on each engine bump.
//
// ref: NVIDIA/DCGM modules/health/DcgmHealthWatch.cpp MonitorNVLinkErrorFields
var nvlinkLifetimeCounterPrefixes = []string{
	"datalink layer CRC error counter",
	"datalink layer recovery error counter",
	"datalink layer replay error counter",
}

// isNVLinkLifetimeCounterIncident reports whether a code-71 NVLink FAIL
// incident was produced by the lifetime-absolute counter check rather than an
// active-fault path. Lifetime-counter incidents are not evidence of a currently
// degraded link on pre-Blackwell GPUs (Hopper/H100): the counter never resets
// without a GPU reset, so any historical CRC event triggers the check.
func isNVLinkLifetimeCounterIncident(inc dcgmapi.Incident) bool {
	if inc.Error.Code != dcgmapi.DCGM_FR_NVLINK_ERROR_CRITICAL {
		return false
	}
	if inc.System != dcgmapi.DCGM_HEALTH_WATCH_NVLINK {
		return false
	}
	for _, prefix := range nvlinkLifetimeCounterPrefixes {
		if strings.Contains(inc.Error.Message, prefix) {
			return true
		}
	}
	return false
}

func (s *DCGMSystem) HealthCheck(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	healthRes, err := s.dcgm.HealthCheck()
	if err != nil {
		if errors.Is(err, ErrNotInitialized) {
			logger.V(2).Info("could not run health check. DCGM is not yet initialized")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to call DCGM health check: %w", err)
	}

	var conditions []monitor.Condition

	for _, incidents := range healthRes.Incidents {
		// DCGM_FR_IMEX_UNHEALTHY (122): IMEX is only for NVLink multi-node systems
		// (GB200 NVL72, DGX/HGX multi-node). Skip on standard GPU instances.
		// ref: https://docs.nvidia.com/multi-node-nvlink-systems/imex-guide/overview.html
		if incidents.Error.Code == 122 {
			logger.V(2).Info("ignoring IMEX health code on non-NVLink multi-node system", "code", incidents.Error.Code)
			continue
		}

		reason := reasons.DCGMHealthCode
		severity := monitor.SeverityWarning
		if incidents.Health == dcgmapi.DCGM_HEALTH_RESULT_FAIL {
			severity = monitor.SeverityFatal
		}

		// DCGM_FR_NVLINK_ERROR_CRITICAL (71) from the lifetime-absolute counter
		// path is not an active-fault signal on pre-Blackwell GPUs: the counter
		// never resets without a GPU reset, so it fires on any historical CRC
		// event. Downgrade to Warning so the node stays schedulable. The
		// rate-delta and Blackwell recovery-event paths (also code 71) emit
		// different messages and remain Fatal.
		if incidents.Health == dcgmapi.DCGM_HEALTH_RESULT_FAIL && isNVLinkLifetimeCounterIncident(incidents) {
			logger.V(2).Info("downgrading NVLink lifetime counter incident to Warning", "message", incidents.Error.Message)
			severity = monitor.SeverityWarning
		}

		// DCGM_FR_FABRIC_PROBE_STATE (123) is DCGM's verdict on the Fabric
		// Manager status field (DCGM_FI_DEV_FABRIC_MANAGER_STATUS), which
		// WatchFields already classifies through Classify. DCGM fails on
		// statuses NMA deliberately does not treat as fatal (NotStarted, and the
		// driver/DCGM version mismatches NvmlTooOld, Unrecognized, and unknown
		// values), so report it as a Warning and leave the Fatal decision to the
		// field check, which still reports a fabric training Failure as Fatal.
		// ref: https://github.com/NVIDIA/DCGM/blob/64df9f894541e426e416131a9820cae97aa4dd81/modules/health/DcgmHealthWatch.cpp#L3676-L3734 (v4.6.1)
		if incidents.Health == dcgmapi.DCGM_HEALTH_RESULT_FAIL && incidents.Error.Code == dcgmapi.DCGM_FR_FABRIC_PROBE_STATE {
			logger.V(2).Info("downgrading fabric probe state incident to Warning", "message", incidents.Error.Message)
			severity = monitor.SeverityWarning
		}

		// health check codes comes from the following:
		// https://github.com/NVIDIA/DCGM/blob/d47c0b77920f8dbfef588eaac2cbbea3401ef463/dcgmlib/dcgm_errors.h#L31
		conditions = append(conditions,
			reason.
				Builder(incidents.Error.Code).
				Message(fmt.Sprintf("DCGM detected issues in health check system with error code %d", incidents.Error.Code)).
				Severity(severity).
				Build(),
		)
	}

	// DCGM reports the fabric probe state on every call (and once per GPU) for
	// as long as the Fabric Manager status is unchanged, so repeats of its
	// Warning are rate-limited.
	fabricProbeState := reasons.DCGMHealthCode.Builder(dcgmapi.DCGM_FR_FABRIC_PROBE_STATE).Build().Reason
	return suppressRepeatedWarnings(s.healthWarnings, s.now(), conditions, func(c monitor.Condition) bool {
		return c.Reason == fabricProbeState
	}), nil
}
