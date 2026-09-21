//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
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

	fsDeviceCount, fsDevicePath, err := GetNvidiaFSDeviceCount()
	if err != nil {
		return nil, fmt.Errorf("failed to get nvidia device count from %s", fsDevicePath)
	}

	var conditions []monitor.Condition

	if gpuDeviceCount != fsDeviceCount {
		conditions = append(conditions,
			reasons.NvidiaDeviceCountMismatch.
				Builder().
				Message(fmt.Sprintf("DCGM detected %d GPUs but %d nvidia device files were detected at %s", gpuDeviceCount, fsDeviceCount, fsDevicePath)).
				Build(),
		)
	}

	// Compare the detected GPU count against the expected count for the EC2
	// instance type. This catches cases where a GPU fails to enumerate on the
	// PCIe bus at boot — both DCGM and /dev will agree on the (wrong) lower
	// count, so the check above won't fire.
	if s.instanceTypeInfoProvider != nil {
		info, err := s.instanceTypeInfoProvider.GetInstanceInfo(ctx)
		if errors.Is(err, instanceinfo.ErrUnknownInstanceType) {
			logger.V(4).Info("instance type not in embedded lookup, skipping GPU count validation", "error", err)
		} else if err != nil {
			logger.V(2).Info("could not determine expected GPU count for validation", "error", err)
		} else if gpuDeviceCount < info.NvidiaGPUCount {
			conditions = append(conditions,
				reasons.NvidiaDeviceCountMismatch.
					Builder().
					Message(fmt.Sprintf("expected %d GPUs for this instance type but only %d were detected — possible hardware failure",
						info.NvidiaGPUCount, gpuDeviceCount)).
					Build(),
			)
		}
	}

	return conditions, nil
}

// nvidiaDevDirs returns the candidate directories to search for nvidia device
// files, ordered by priority: explicit override, GPU Operator default, /dev.
func nvidiaDevDirs() []string {
	if root := config.NvidiaDriverRoot(); root != "" {
		return []string{config.ToHostPath(filepath.Join(root, "dev"))}
	}
	gpuOpDir := config.ToHostPath(filepath.Join(config.DefaultGPUOperatorDriverRoot, "dev"))
	standardDir := config.ToHostPath("/dev")
	if info, err := os.Stat(gpuOpDir); err == nil && info.IsDir() {
		return []string{gpuOpDir, standardDir}
	}
	return []string{standardDir}
}

// GetNvidiaFSDeviceCount globs candidate directories and returns the count
// from whichever directory contains the most nvidia device files.
func GetNvidiaFSDeviceCount() (uint, string, error) {
	var bestCount uint
	var bestDir string
	for _, dir := range nvidiaDevDirs() {
		paths, err := filepath.Glob(filepath.Join(dir, "nvidia[0-9]*"))
		if err != nil {
			return 0, dir, err
		}
		if count := uint(len(paths)); count > bestCount {
			bestCount = count
			bestDir = dir
		}
	}
	if bestDir == "" {
		dirs := nvidiaDevDirs()
		bestDir = dirs[len(dirs)-1]
	}
	return bestCount, bestDir, nil
}
