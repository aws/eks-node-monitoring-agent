package ebsnvme

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Device represents an NVMe device
type Device struct {
	Path string
}

// NewDevice creates a new NVMe device instance
func NewDevice(path string) *Device {
	return &Device{Path: path}
}

// NvmeIoctl performs an NVMe ioctl operation
func (d *Device) NvmeIoctl(adminCmd *NvmeAdminCommand) error {
	file, err := os.OpenFile(d.Path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open device: %v", err)
	}
	defer file.Close()

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		file.Fd(),
		NVME_IOCTL_ADMIN_CMD,
		uintptr(unsafe.Pointer(adminCmd)),
	)

	if errno != 0 {
		return fmt.Errorf("failed to issue nvme cmd, err: %v", errno)
	}

	return nil
}

// BytesToString converts a byte array to string and trims nulls
func BytesToString(b []byte) string {
	return strings.TrimRight(string(b), "\x00")
}
