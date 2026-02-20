package ebsnvme

import (
	"unsafe"
)

// StatsDevice represents an NVMe device for stats collection
type StatsDevice struct {
	*Device
}

// NewStatsDevice creates a new NVMe stats device
func NewStatsDevice(path string) *StatsDevice {
	return &StatsDevice{
		Device: NewDevice(path),
	}
}

// QueryStatsFromDevice queries stats from the device
func (d *StatsDevice) QueryStatsFromDevice() (*NvmeGetAmznStatsLogpage, error) {
	stats := &NvmeGetAmznStatsLogpage{}
	adminCmd := NvmeAdminCommand{
		Opcode: NVME_GET_LOG_PAGE,
		Addr:   uint64(uintptr(unsafe.Pointer(stats))),
		Alen:   uint32(unsafe.Sizeof(*stats)),
		Nsid:   1,
		Cdw10:  uint32(AMZN_NVME_STATS_LOGPAGE_ID | (1024 << 16)),
	}

	if err := d.NvmeIoctl(&adminCmd); err != nil {
		return nil, err
	}

	return stats, nil
}
