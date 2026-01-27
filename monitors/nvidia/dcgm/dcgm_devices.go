//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
)

func (s *DCGMSystem) DeviceCount(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	gpuDeviceCount, err := s.dcgm.GetDeviceCount()
	if err != nil {
		if errors.Is(err, ErrNotInitialized) {
			logger.V(2).Info("could not get device count. DCGM is not yet initialized")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to call DCGM get device count: %w", err)
	}

	fsDeviceCount, err := GetNvidiaFSDeviceCount()
	if err != nil {
		return nil, fmt.Errorf("failed to get nvidia device count from /dev directory")
	}

	var conditions []monitor.Condition

	if gpuDeviceCount != fsDeviceCount {
		conditions = append(conditions,
			reasons.NvidiaDeviceCountMismatch.
				Builder().
				Message(fmt.Sprintf("DCGM detected %d GPUs but %d nvidia device files were detected", gpuDeviceCount, fsDeviceCount)).
				Build(),
		)
	}

	return conditions, nil
}

func GetNvidiaFSDeviceCount() (uint, error) {
	paths, err := filepath.Glob(config.ToHostPath("/dev/nvidia[0-9]*"))
	return uint(len(paths)), err
}
