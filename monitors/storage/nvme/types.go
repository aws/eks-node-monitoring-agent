package ebsnvme

import "strings"

// NVMe Admin Command structure
type NvmeAdminCommand struct {
	Opcode    uint8
	Flags     uint8
	Cid       uint16
	Nsid      uint32
	Reserved0 uint64
	Mptr      uint64
	Addr      uint64
	Mlen      uint32
	Alen      uint32
	Cdw10     uint32
	Cdw11     uint32
	Cdw12     uint32
	Cdw13     uint32
	Cdw14     uint32
	Cdw15     uint32
	Reserved1 uint64
}

// NVMe Identify Controller Amazon VS structure
type NvmeIdentifyControllerAmznVs struct {
	Bdev      [32]byte
	Reserved0 [1024 - 32]byte
}

// NVMe Identify Controller PSD structure
type NvmeIdentifyControllerPsd struct {
	Mp        uint16
	Reserved0 uint16
	Enlat     uint32
	Exlat     uint32
	Rrt       uint8
	Rrl       uint8
	Rwt       uint8
	Rwl       uint8
	Reserved1 [16]byte
}

// NVMe Identify Controller structure
type NvmeIdentifyController struct {
	Vid       uint16
	Ssvid     uint16
	Sn        [20]byte
	Mn        [40]byte
	Fr        [8]byte
	Rab       uint8
	Ieee      [3]byte
	Mic       uint8
	Mdts      uint8
	Reserved0 [256 - 78]byte
	Oacs      uint16
	Acl       uint8
	Aerl      uint8
	Frmw      uint8
	Lpa       uint8
	Elpe      uint8
	Npss      uint8
	Avscc     uint8
	Reserved1 [512 - 265]byte
	Sqes      uint8
	Cqes      uint8
	Reserved2 uint16
	Nn        uint32
	Oncs      uint16
	Fuses     uint16
	Fna       uint8
	Vwc       uint8
	Awun      uint16
	Awupf     uint16
	Nvscc     uint8
	Reserved3 [704 - 531]byte
	Reserved4 [2048 - 704]byte
	Psd       [32]NvmeIdentifyControllerPsd
	Vs        NvmeIdentifyControllerAmznVs
}

// GetVolumeId gets volume ID from controller info
func (id *NvmeIdentifyController) GetVolumeId() string {
	vol := BytesToString(id.Sn[:])
	if strings.HasPrefix(vol, "vol") && vol[3] != '-' {
		vol = "vol-" + vol[3:]
	}
	return vol
}

// GetBlockDevice gets block device from controller info
func (id *NvmeIdentifyController) GetBlockDevice() string {
	return strings.TrimSpace(BytesToString(id.Vs.Bdev[:]))
}

// GetModelNumber gets the model number for the NVMe subsystem that is assigned by the vendor as an ASCII string.
func (id *NvmeIdentifyController) GetModelNumber() string {
	return strings.TrimSpace(BytesToString(id.Mn[:]))
}

// NVMe Histogram Bin structure
type NvmeHistogramBin struct {
	Lower     uint64
	Upper     uint64
	Count     uint32
	Reserved0 uint32
}

// EBS NVMe Histogram structure
type EbsNvmeHistogram struct {
	NumBins uint64
	Bins    [64]NvmeHistogramBin
}

// NVMe Get Amazon Stats Log Page structure
type NvmeGetAmznStatsLogpage struct {
	Magic                                 uint32
	Reserved0                             [4]byte
	TotalReadOps                          uint64
	TotalWriteOps                         uint64
	TotalReadBytes                        uint64
	TotalWriteBytes                       uint64
	TotalReadTime                         uint64
	TotalWriteTime                        uint64
	EbsVolumePerformanceExceededIops      uint64
	EbsVolumePerformanceExceededTp        uint64
	Ec2InstanceEbsPerformanceExceededIops uint64
	Ec2InstanceEbsPerformanceExceededTp   uint64
	VolumeQueueLength                     uint64
	Reserved1                             [416]byte
	ReadIoLatencyHistogram                EbsNvmeHistogram
	WriteIoLatencyHistogram               EbsNvmeHistogram
	Reserved2                             [496]byte
}
