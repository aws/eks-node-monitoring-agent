//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
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
