//go:build !darwin

package dcgm

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
)

func (s *DCGMSystem) Reconcile(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	justInit, err := s.dcgm.Reconcile(ctx)
	if err != nil {
		logger.Error(err, "failed to reconcile DCGM state")
		return []monitor.Condition{
			reasons.DCGMError.
				Builder().
				Message(err.Error()).
				Build(),
		}, nil
	}

	if justInit {
		logger.V(2).Info("DCGM was initialized")
	}

	return nil, nil
}
