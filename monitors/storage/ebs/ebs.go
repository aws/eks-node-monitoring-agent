package ebs

import (
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/storage/nvme"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
)

func NewEBSSystem() *ebsNVMeSystem {
	return &ebsNVMeSystem{
		runtimeCtx:                     config.GetRuntimeContext(),
		lastExceededVolumeIops:         map[string]uint64{},
		lastExceededVolumeThroughput:   map[string]uint64{},
		lastExceededInstanceIops:       map[string]uint64{},
		lastExceededInstanceThroughput: map[string]uint64{},
		deviceControllerFn:             func(device *ebsnvme.Device) DeviceController { return &ebsNVMeDeviceController{device} },
	}
}

type ebsNVMeSystem struct {
	runtimeCtx *config.RuntimeContext

	lastExceededVolumeIops         map[string]uint64
	lastExceededVolumeThroughput   map[string]uint64
	lastExceededInstanceIops       map[string]uint64
	lastExceededInstanceThroughput map[string]uint64

	deviceControllerFn func(*ebsnvme.Device) DeviceController
}
