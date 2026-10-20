//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/devicefiles"
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

	fsDeviceCount, fsDevicePath, err := devicefiles.Count()
	if err != nil {
		return nil, fmt.Errorf("failed to get nvidia device count from %s", fsDevicePath)
	}

	signals := classify.NormalizedSignals{GPUCount: &gpuDeviceCount, GPUDeviceFileCount: &fsDeviceCount, GPUDeviceFilePath: fsDevicePath}

	// Compare the detected GPU count against the expected count for the EC2
	// instance type. This catches cases where a GPU fails to enumerate on the
	// PCIe bus at boot — both DCGM and /dev will agree on the (wrong) lower
	// count, so the check above won't fire.
	// Classification of both checks is delegated to classify.Classify.
	if s.instanceTypeInfoProvider != nil {
		info, err := s.instanceTypeInfoProvider.GetInstanceInfo(ctx)
		if errors.Is(err, instanceinfo.ErrUnknownInstanceType) {
			logger.V(4).Info("instance type not in embedded lookup, skipping GPU count validation", "error", err)
		} else if err != nil {
			logger.V(2).Info("could not determine expected GPU count for validation", "error", err)
		} else {
			signals.ExpectedGPUCount = &info.NvidiaGPUCount
		}
	}

	return classify.Classify(signals), nil
}
