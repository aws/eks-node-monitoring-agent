//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
)

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
		reason := reasons.DCGMHealthCode
		severity := monitor.SeverityWarning
		if incidents.Health == dcgmapi.DCGM_HEALTH_RESULT_FAIL {
			severity = monitor.SeverityFatal
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
