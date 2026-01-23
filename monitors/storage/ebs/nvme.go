package ebs

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/storage/nvme"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const ebsThrottlingPeriod = 10 * time.Minute

// microseconds of throttling allowed per period before reporting.
//
// thresholds are parametrized by the throttling period, since the jump in
// measured throttling depends on the time between invocations.
var (
	ThresholdEbsVolumePerformanceExceededIops      = 5 * float64(time.Second.Microseconds()) * ebsThrottlingPeriod.Minutes()
	ThresholdEbsVolumePerformanceExceededTp        = 5 * float64(time.Second.Microseconds()) * ebsThrottlingPeriod.Minutes()
	ThresholdEc2InstanceEbsPerformanceExceededIops = 5 * float64(time.Second.Microseconds()) * ebsThrottlingPeriod.Minutes()
	ThresholdEc2InstanceEbsPerformanceExceededTp   = 5 * float64(time.Second.Microseconds()) * ebsThrottlingPeriod.Minutes()
)

type DeviceController interface {
	QueryIdCtrlFromDevice() (*ebsnvme.NvmeIdentifyController, error)
	QueryStatsFromDevice() (*ebsnvme.NvmeGetAmznStatsLogpage, error)
}

type ebsNVMeDeviceController struct {
	device *ebsnvme.Device
}

func (e *ebsNVMeDeviceController) QueryStatsFromDevice() (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
	statsDevice := ebsnvme.StatsDevice{Device: e.device}
	return statsDevice.QueryStatsFromDevice()
}

func (e *ebsNVMeDeviceController) QueryIdCtrlFromDevice() (*ebsnvme.NvmeIdentifyController, error) {
	identityDevice := ebsnvme.IdDevice{Device: e.device}
	return identityDevice.QueryIdCtrlFromDevice()
}

func (s *ebsNVMeSystem) NVMeThrottles(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	devicePaths, err := filepath.Glob(config.ToHostPath("/dev/nvme*"))
	if err != nil {
		return nil, fmt.Errorf("discovering NVME devices, %w", err)
	}

	var conditions []monitor.Condition

	checkedVolumes := set.New[string]()

	for _, devicePath := range devicePaths {
		nvmeDevice := ebsnvme.NewDevice(devicePath)
		deviceController := s.deviceControllerFn(nvmeDevice)

		idInfo, err := deviceController.QueryIdCtrlFromDevice()
		if err != nil {
			logger.V(2).Info("ignoring device due to inability to ID", "device", devicePath, "error", err)
			continue
		}

		if idInfo.Vid != ebsnvme.AMZN_NVME_VID || idInfo.GetModelNumber() != ebsnvme.AMZN_NVME_EBS_MN {
			logger.V(6).Info("ignoring non-ebs device", "nvmeId", idInfo.Vid, "modelNumber", idInfo.GetModelNumber())
			continue
		}

		volumeID := idInfo.GetVolumeId()
		blockDeviceName := idInfo.GetBlockDevice()

		if checkedVolumes.Has(volumeID) {
			continue
		}
		checkedVolumes.Insert(volumeID)

		stats, err := deviceController.QueryStatsFromDevice()
		if err != nil {
			logger.V(2).Info("ignoring device due to inability to query stats", "device", devicePath, "error", err)
			continue
		}

		if stats.Magic != ebsnvme.AMZN_NVME_STATS_MAGIC {
			logger.V(4).Info("ignoring device due to incorrect magic bytes value", "magic", stats.Magic)
			continue
		}

		conditions = append(conditions, s.checkVolumeStatistics(stats, volumeID, blockDeviceName)...)
	}

	return conditions, err
}

func (s *ebsNVMeSystem) checkVolumeStatistics(stats *ebsnvme.NvmeGetAmznStatsLogpage, volumeID string, blockDeviceName string) []monitor.Condition {
	var conditions []monitor.Condition

	if diff := stats.EbsVolumePerformanceExceededIops - s.lastExceededVolumeIops[volumeID]; diff > 0 {
		s.lastExceededVolumeIops[volumeID] = stats.EbsVolumePerformanceExceededIops
		if diff > uint64(ThresholdEbsVolumePerformanceExceededIops) {
			conditions = append(conditions,
				reasons.EBSVolumeIOPSExceeded.
					Builder().
					Message(fmt.Sprintf("Volume %s (%s) reported exceeded configured IOPS for %s", volumeID, blockDeviceName, time.Duration(diff)*time.Microsecond)).
					Build(),
			)
		}
	}

	if diff := stats.EbsVolumePerformanceExceededTp - s.lastExceededVolumeThroughput[volumeID]; diff > 0 {
		s.lastExceededVolumeThroughput[volumeID] = stats.EbsVolumePerformanceExceededTp
		if diff > uint64(ThresholdEbsVolumePerformanceExceededTp) {
			conditions = append(conditions,
				reasons.EBSVolumeThroughputExceeded.
					Builder().
					Message(fmt.Sprintf("Volume %s (%s) reported exceeded configured throughput for %s", volumeID, blockDeviceName, time.Duration(diff)*time.Microsecond)).
					Build(),
			)
		}
	}

	if diff := stats.Ec2InstanceEbsPerformanceExceededIops - s.lastExceededInstanceIops[volumeID]; diff > 0 {
		s.lastExceededInstanceIops[volumeID] = stats.Ec2InstanceEbsPerformanceExceededIops
		if diff > uint64(ThresholdEc2InstanceEbsPerformanceExceededIops) {
			conditions = append(conditions,
				reasons.EBSInstanceIOPSExceeded.
					Builder().
					Message(fmt.Sprintf("Volume %s (%s) reported exceeded instance IOPS for %s", volumeID, blockDeviceName, time.Duration(diff)*time.Microsecond)).
					Build(),
			)
		}
	}

	if diff := stats.Ec2InstanceEbsPerformanceExceededTp - s.lastExceededInstanceThroughput[volumeID]; diff > 0 {
		s.lastExceededInstanceThroughput[volumeID] = stats.Ec2InstanceEbsPerformanceExceededTp
		if diff > uint64(ThresholdEc2InstanceEbsPerformanceExceededTp) {
			conditions = append(conditions,
				reasons.EBSInstanceThroughputExceeded.
					Builder().
					Message(fmt.Sprintf("Volume %s (%s) reported exceeded instance throughput for %s", volumeID, blockDeviceName, time.Duration(diff)*time.Microsecond)).
					Build(),
			)
		}
	}

	return conditions
}
