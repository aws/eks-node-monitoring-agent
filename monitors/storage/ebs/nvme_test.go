package ebs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/storage/nvme"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
)

func TestEbsThrottling(t *testing.T) {
	t.Run("No Devices", func(t *testing.T) {
		SetupRoot(t)
		SetupNVMe(t, "foo")
		ebsSystem := NewEBSSystem()
		conditions, err := ebsSystem.NVMeThrottles(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("No Throttling", func(t *testing.T) {
		SetupRoot(t)
		SetupNVMe(t, "nvmefoo")
		ebsSystem := NewEBSSystem()
		conditions, err := ebsSystem.NVMeThrottles(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("Not EBS Device", func(t *testing.T) {
		SetupRoot(t)
		SetupNVMe(t, "nvmefoo")

		ebsSystem := NewEBSSystem()

		t.Run("Bad VID", func(t *testing.T) {
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				IdFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeIdentifyController) (*ebsnvme.NvmeIdentifyController, error) {
					src.Vid = 0
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 0)
		})

		t.Run("Bad ModelNumber", func(t *testing.T) {
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				IdFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeIdentifyController) (*ebsnvme.NvmeIdentifyController, error) {
					src.Mn = [40]byte{}
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 0)
		})
	})

	t.Run("Throttling", func(t *testing.T) {
		SetupRoot(t)
		SetupNVMe(t, "nvmefoo")

		t.Run("Volume IOPS", func(t *testing.T) {
			ebsSystem := NewEBSSystem()
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				StatsFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeGetAmznStatsLogpage) (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
					src.EbsVolumePerformanceExceededIops = uint64(ThresholdEbsVolumePerformanceExceededIops) + 1
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1)
			assert.Equal(t, conditions[0], monitor.Condition{
				Reason:   "EBSVolumeIOPSExceeded",
				Message:  "Volume foo (bar) reported exceeded configured IOPS for 50.000001s",
				Severity: monitor.SeverityWarning,
			})
		})

		t.Run("Volume Throughput", func(t *testing.T) {
			ebsSystem := NewEBSSystem()
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				StatsFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeGetAmznStatsLogpage) (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
					src.EbsVolumePerformanceExceededTp = uint64(ThresholdEbsVolumePerformanceExceededTp) + 1
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1)
			assert.Equal(t, conditions[0], monitor.Condition{
				Reason:   "EBSVolumeThroughputExceeded",
				Message:  "Volume foo (bar) reported exceeded configured throughput for 50.000001s",
				Severity: monitor.SeverityWarning,
			})
		})

		t.Run("Instance IOPS", func(t *testing.T) {
			ebsSystem := NewEBSSystem()
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				StatsFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeGetAmznStatsLogpage) (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
					src.Ec2InstanceEbsPerformanceExceededIops = uint64(ThresholdEc2InstanceEbsPerformanceExceededIops) + 1
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1)
			assert.Equal(t, conditions[0], monitor.Condition{
				Reason:   "EBSInstanceIOPSExceeded",
				Message:  "Volume foo (bar) reported exceeded instance IOPS for 50.000001s",
				Severity: monitor.SeverityWarning,
			})
		})

		t.Run("Instance Throughput", func(t *testing.T) {
			ebsSystem := NewEBSSystem()
			ebsSystem.deviceControllerFn = makeDeviceControllerFn(&fakeDeviceController{
				StatsFn: func(device *ebsnvme.Device, src *ebsnvme.NvmeGetAmznStatsLogpage) (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
					src.Ec2InstanceEbsPerformanceExceededTp = uint64(ThresholdEc2InstanceEbsPerformanceExceededTp) + 1
					return src, nil
				},
			})
			conditions, err := ebsSystem.NVMeThrottles(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1)
			assert.Equal(t, conditions[0], monitor.Condition{
				Reason:   "EBSInstanceThroughputExceeded",
				Message:  "Volume foo (bar) reported exceeded instance throughput for 50.000001s",
				Severity: monitor.SeverityWarning,
			})
		})
	})
}

func makeDeviceControllerFn(controller *fakeDeviceController) func(*ebsnvme.Device) DeviceController {
	return func(device *ebsnvme.Device) DeviceController {
		controller.device = device
		return controller
	}
}

type fakeDeviceController struct {
	device  *ebsnvme.Device
	IdFn    func(device *ebsnvme.Device, src *ebsnvme.NvmeIdentifyController) (*ebsnvme.NvmeIdentifyController, error)
	StatsFn func(device *ebsnvme.Device, src *ebsnvme.NvmeGetAmznStatsLogpage) (*ebsnvme.NvmeGetAmznStatsLogpage, error)
}

func (f *fakeDeviceController) QueryIdCtrlFromDevice() (*ebsnvme.NvmeIdentifyController, error) {
	id := &ebsnvme.NvmeIdentifyController{
		Vid: ebsnvme.AMZN_NVME_VID,
	}
	copy(id.Mn[:], ebsnvme.AMZN_NVME_EBS_MN)
	copy(id.Sn[:], "foo")
	copy(id.Vs.Bdev[:], "bar")
	if f.IdFn != nil {
		return f.IdFn(f.device, id)
	}
	return id, nil
}

func (f *fakeDeviceController) QueryStatsFromDevice() (*ebsnvme.NvmeGetAmznStatsLogpage, error) {
	stats := &ebsnvme.NvmeGetAmznStatsLogpage{
		Magic: ebsnvme.AMZN_NVME_STATS_MAGIC,
	}
	if f.StatsFn != nil {
		return f.StatsFn(f.device, stats)
	}
	return stats, nil
}

func SetupRoot(t *testing.T) string {
	root := t.TempDir()
	t.Setenv(config.HOST_ROOT_ENV, root)
	return root
}

func SetupNVMe(t *testing.T, deviceName string) {
	root := config.HostRoot()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "dev", deviceName), 0755))
}
