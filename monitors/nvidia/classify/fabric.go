package classify

import "fmt"

// Fabric Manager status values from dcgmFabricManagerStatus_t (dcgm_structs.h).
// ref: https://github.com/NVIDIA/DCGM/blob/64df9f894541e426e416131a9820cae97aa4dd81/dcgmlib/dcgm_structs.h#L4310-L4316 (v4.6.1)
const (
	DcgmFMStatusNotSupported int64 = 0
	DcgmFMStatusNotStarted   int64 = 1
	DcgmFMStatusInProgress   int64 = 2
	DcgmFMStatusSuccess      int64 = 3
	DcgmFMStatusFailure      int64 = 4
	DcgmFMStatusUnrecognized int64 = 5
	DcgmFMStatusNvmlTooOld   int64 = 6
)

// dcgmStatusBadParam is DCGM_ST_BADPARAM. DCGM stores it in the Fabric Manager
// status field, instead of Unrecognized, when NVML reports a fabric state DCGM
// does not know.
// ref: https://github.com/NVIDIA/go-dcgm/blob/v1.4601.1/pkg/dcgm/const.go#L82
const dcgmStatusBadParam int64 = -1

// dcgmInt64Blank is DCGM_FT_INT64_BLANK, the lowest of DCGM's int64 blank
// values. DCGM stores it, or one of the three values above it (NOT_FOUND,
// NOT_SUPPORTED, NOT_PERMISSIONED), when it cannot read a field.
// ref: https://github.com/NVIDIA/go-dcgm/blob/v1.4601.1/pkg/dcgm/utils.go#L17
const dcgmInt64Blank int64 = 0x7ffffffffffffff0

// isInt64Blank reports whether v is one of DCGM's int64 blank values, matching
// go-dcgm's IsInt64Blank.
func isInt64Blank(v int64) bool { return v >= dcgmInt64Blank }

var fabricManagerStatusNames = map[int64]string{
	DcgmFMStatusNotSupported: "NotSupported",
	DcgmFMStatusNotStarted:   "NotStarted",
	DcgmFMStatusInProgress:   "InProgress",
	DcgmFMStatusSuccess:      "Success",
	DcgmFMStatusFailure:      "Failure",
	DcgmFMStatusUnrecognized: "Unrecognized",
	DcgmFMStatusNvmlTooOld:   "NvmlTooOld",
}

// FabricHealthMaskHasFault reports whether a fabric health mask (field 174)
// decodes as a fault. A blank value is not a fault.
func FabricHealthMaskHasFault(mask uint64) bool {
	return !isInt64Blank(int64(mask)) && len(fabricHealthMaskFaults(int64(mask))) > 0
}

// fabricHealthMaskFaults decodes the faulting sub-fields of a fabric health
// mask (field 174). Each sub-field is extracted as (mask >> shift) & widthMask
// (mirroring NVIDIA's DCGM_GPU_FABRIC_HEALTH_TEST macro) and reported only when
// it is in a fault state. Returns the faults as "name=value" strings, or an
// empty slice when the mask indicates no fault.
func fabricHealthMaskFaults(mask int64) []string {
	var faults []string
	for _, sf := range fabricHealthSubFields {
		v := (mask >> sf.shift) & sf.widthMask
		if sf.fault(v) {
			faults = append(faults, fmt.Sprintf("%s=%d", sf.name, v))
		}
	}
	return faults
}

// fabricHealthSubField describes one sub-field packed into the
// DCGM_FI_DEV_FABRIC_HEALTH_MASK value. Each sub-field is decoded as
// (mask >> shift) & widthMask, matching NVML_GPU_FABRIC_HEALTH_GET. For the
// boolean sub-fields the value encoding is 0=NotSupported, 1=True, 2=False,
// so only value 1 (the fault asserted) is flagged; NotSupported and False
// are healthy. A healthy GPU can therefore report a non-zero mask, e.g. 0x80
// decodes to access_timeout_recovery=False.
//
// The shift/width/value defines are sourced from NVIDIA's published NVML API
// reference; note the nvml.h vendored via go-dcgm only defines the DEGRADED_BW
// sub-field and lists a stale WIDTH of 0x11.
// ref: https://docs.nvidia.com/deploy/nvml-api/group__nvmlFabricDefs.html (NVML_GPU_FABRIC_HEALTH_MASK_*, NVML_GPU_FABRIC_HEALTH_GET)
// ref: https://github.com/NVIDIA/DCGM/blob/master/dcgmlib/dcgm_fields.h (DCGM_FI_DEV_FABRIC_HEALTH_MASK = 174)
type fabricHealthSubField struct {
	name      string
	shift     uint
	widthMask int64
	fault     func(v int64) bool
}

// faultWhenTrue reports a fault only for the True state (value 1).
func faultWhenTrue(v int64) bool { return v == 1 }

var fabricHealthSubFields = []fabricHealthSubField{
	// Boolean sub-fields: 0=NotSupported, 1=True (fault), 2=False (healthy).
	{name: "degraded_bw", shift: 0, widthMask: 0x3, fault: faultWhenTrue},
	{name: "route_recovery", shift: 2, widthMask: 0x3, fault: faultWhenTrue},
	{name: "route_unhealthy", shift: 4, widthMask: 0x3, fault: faultWhenTrue},
	{name: "access_timeout_recovery", shift: 6, widthMask: 0x3, fault: faultWhenTrue},
	// Incorrect configuration: 0=NotSupported, 1=None (correct), >=2=incorrect.
	{name: "incorrect_configuration", shift: 8, widthMask: 0xf, fault: func(v int64) bool { return v >= 2 }},
	// partition_assigned (shift 12) is informational, not a health fault, so
	// it is intentionally omitted.
}
