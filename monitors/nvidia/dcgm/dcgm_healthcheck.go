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

	return conditions, nil
}
