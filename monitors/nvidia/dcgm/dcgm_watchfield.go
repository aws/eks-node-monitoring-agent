//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

func (s *DCGMSystem) WatchFields(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	sinceTime := time.Now().Add(-s.fieldValueWindow)
	fieldValues, err := s.dcgm.GetValuesSince(sinceTime)
	if err != nil {
		if errors.Is(err, ErrNotInitialized) {
			logger.V(2).Info("could not get field values. DCGM is not yet initialized")
			return nil, nil
		}
		return nil, err
	}

	var conditions []monitor.Condition

	for _, fieldValue := range fieldValues {
		// Fabric fields encode health in the field value, not in fieldValue.Status,
		// so they must be handled before the OK/NO_DATA gate below. DCGM returns
		// DCGM_ST_OK for a successfully-read fabric field even when the value
		// indicates Failure (e.g. FM_STATUS=Failure on a node where
		// nvidia-fabricmanager.service has exited). The handler suppresses
		// emission for healthy/unsupported states internally.
		if c, handled := handleFabricField(fieldValue); handled {
			if c != nil {
				conditions = append(conditions, *c)
			}
			continue
		}

		// skip if there's no issue with the field or no new data was provided.
		if fieldValue.Status == dcgmapi.DCGM_ST_OK || fieldValue.Status == dcgmapi.DCGM_ST_NO_DATA {
			continue
		}

		conditionMessage := fmt.Sprintf("DCGM detected fieldID %d with statusCode %d", fieldValue.FieldID, fieldValue.Status)

		fieldValueMapper, ok := fieldValueMappers[fieldValue.FieldID]
		if !ok {
			// NOTE: if this happens, then there is a gap between the watchers
			// registered with dcgm and what is expected here.
			logger.V(4).Info("ignoring field with missing handler", "fieldId", fieldValue.FieldID)
			continue
		}
		if ok, reason := fieldValueMapper(fieldValue); ok {
			conditionMessage += fmt.Sprintf(": %s", reason)
		}

		conditions = append(conditions,
			reasons.DCGMFieldError.
				Builder(fieldValue.FieldID).
				Message(conditionMessage).
				Build(),
		)
	}

	return conditions, nil
}

// handleFabricField checks whether the field is a fabric-related field and
// returns the appropriate condition. Returns (nil, true) when the field is
// recognized but healthy, and (*condition, true) when unhealthy. Returns
// (nil, false) when the field is not a fabric field.
func handleFabricField(fv dcgmapi.FieldValue_v2) (*monitor.Condition, bool) {
	switch fv.FieldID {
	case dcgmapi.DCGM_FI_DEV_FABRIC_MANAGER_STATUS:
		status := fv.Int64()
		// NotSupported: FM not applicable (e.g. single-GPU, or rack-level NVSwitch like GB200/GB300).
		// NotStarted: FM hasn't started yet; skipped to avoid false positives on instances
		// where FM is not needed but DCGM doesn't report NotSupported (e.g. GB200/GB300).
		// InProgress: FM is still performing fabric training during boot.
		// Success: FM is running and healthy.
		if status == DcgmFMStatusSuccess || status == DcgmFMStatusNotSupported ||
			status == DcgmFMStatusInProgress || status == DcgmFMStatusNotStarted {
			return nil, true
		}
		name := fabricManagerStatusNames[status]
		if name == "" {
			name = fmt.Sprintf("Unknown(%d)", status)
		}
		c := reasons.FabricManagerNotRunning.
			Builder().
			Message(fmt.Sprintf("Fabric Manager status: %s", name)).
			Build()
		return &c, true
	case dcgmapi.DCGM_FI_DEV_FABRIC_HEALTH_MASK:
		mask := fv.Int64()
		// The mask packs several 2-bit sub-fields, each decoded as
		// (mask >> shift) & widthMask following NVIDIA's
		// DCGM_GPU_FABRIC_HEALTH_TEST macro. A non-zero mask is NOT sufficient
		// to declare a fault: the boolean sub-fields encode 0=NotSupported,
		// 1=True (fault asserted), 2=False (explicitly healthy), so a healthy
		// GPU can report a non-zero mask (e.g. 0x80 decodes to
		// access_timeout_recovery=False). Only the True/fault state of a
		// sub-field is flagged; this is what eliminates the false positives
		// from the previous "any non-zero mask is a fault" logic.
		faults := fabricHealthMaskFaults(mask)
		if len(faults) == 0 {
			return nil, true
		}
		c := reasons.NvidiaFabricError.
			Builder().
			Message(fmt.Sprintf("GPU fabric health mask 0x%x: %s", mask, strings.Join(faults, ", "))).
			Build()
		return &c, true
	default:
		return nil, false
	}
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

// ref: https://github.com/NVIDIA/DCGM/blob/6e947dcac9b3160d61d98fea4741d51d4bec5c1f/dcgmlib/dcgm_fields.h#L99-L103
const (
	// Nothing is running on the GPU and the clocks are dropping to Idle state
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE int64 = 0x0000000000000001
	// GPU clocks are limited by current setting of applications clocks
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING int64 = 0x0000000000000002
	// SW Power Scaling algorithm is reducing the clocks below requested clocks
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP int64 = 0x0000000000000004
	// HW Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN int64 = 0x0000000000000008
	// Sync Boost
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST int64 = 0x0000000000000010
	// SW Thermal Slowdown
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL int64 = 0x0000000000000020
	// HW Thermal Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL int64 = 0x0000000000000040
	// HW Power Brake Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE int64 = 0x0000000000000080
	// GPU clocks are limited by current setting of Display clocks
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS int64 = 0x0000000000000100
)

// Fabric Manager status values from dcgmFabricManagerStatus_t (dcgm_structs.h).
const (
	DcgmFMStatusNotSupported int64 = 0
	DcgmFMStatusNotStarted   int64 = 1
	DcgmFMStatusInProgress   int64 = 2
	DcgmFMStatusSuccess      int64 = 3
)

// fabricHealthSubField describes one sub-field packed into the
// DCGM_FI_DEV_FABRIC_HEALTH_MASK value. Each sub-field is decoded as
// (mask >> shift) & widthMask, matching NVML_GPU_FABRIC_HEALTH_GET. For the
// boolean sub-fields the value encoding is 0=NotSupported, 1=True, 2=False,
// so only value 1 (the fault asserted) is flagged; NotSupported and False
// are healthy.
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

var clockThrottleReasons = map[int64]string{
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE:       "gpu_idle",
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING: "clocks_setting",
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP:   "sw_power_cap",
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN:    "hw_slowdown",
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST:     "sync_boost",
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL:     "sw_thermal",
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL:     "hw_thermal",
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE: "hw_power_brake",
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS: "display_clocks",
}

var fabricManagerStatusNames = map[int64]string{
	0: "NotSupported",
	1: "NotStarted",
	2: "InProgress",
	3: "Success",
	4: "Failure",
	5: "Unrecognized",
	6: "NvmlTooOld",
}

var fieldValueMappers = map[dcgmapi.Short]func(dcgmapi.FieldValue_v2) (bool, string){
	dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: func(fieldValue dcgmapi.FieldValue_v2) (bool, string) {
		deviceClockReasons := fieldValue.Int64()
		for throttleReasonMask, reason := range clockThrottleReasons {
			if deviceClockReasons&throttleReasonMask != 0 {
				return true, fmt.Sprintf("Clocks Throttle Reason %q", reason)
			}
		}
		return false, ""
	},
	dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS: func(fieldValue dcgmapi.FieldValue_v2) (bool, string) {
		return true, fmt.Sprintf("SXID Fatal Error Code %d", fieldValue.Int64())
	},
	dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS: func(fieldValue dcgmapi.FieldValue_v2) (bool, string) {
		return true, fmt.Sprintf("SXID Non-Fatal Error Code %d", fieldValue.Int64())
	},
}
