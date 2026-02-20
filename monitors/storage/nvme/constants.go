// ebsnvme is ported from https://raw.githubusercontent.com/amazonlinux/amazon-ec2-utils/refs/heads/main/ebsnvme
package ebsnvme

// NVMe command constants
const (
	NVME_ADMIN_IDENTIFY  = 0x06
	NVME_GET_LOG_PAGE    = 0x02
	NVME_IOCTL_ADMIN_CMD = 0xC0484E41
)

// Amazon EBS NVMe constants
const (
	AMZN_NVME_EBS_MN           = "Amazon Elastic Block Store"
	AMZN_NVME_STATS_LOGPAGE_ID = 0xD0
	AMZN_NVME_STATS_MAGIC      = 0x3C23B510
	AMZN_NVME_VID              = 0x1D0F
)
