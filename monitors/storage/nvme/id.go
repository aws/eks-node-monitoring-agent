package ebsnvme

import (
	"unsafe"
)

// IdDevice represents an NVMe device for identification
type IdDevice struct {
	*Device
}

// NewIdDevice creates a new NVMe ID device
func NewIdDevice(path string) *IdDevice {
	return &IdDevice{
		Device: NewDevice(path),
	}
}

// QueryIdCtrlFromDevice queries ID controller from device
func (d *IdDevice) QueryIdCtrlFromDevice() (*NvmeIdentifyController, error) {
	idCtrl := &NvmeIdentifyController{}
	adminCmd := NvmeAdminCommand{
		Opcode: NVME_ADMIN_IDENTIFY,
		Addr:   uint64(uintptr(unsafe.Pointer(idCtrl))),
		Alen:   uint32(unsafe.Sizeof(*idCtrl)),
		Cdw10:  1,
	}

	if err := d.NvmeIoctl(&adminCmd); err != nil {
		return nil, err
	}

	return idCtrl, nil
}
