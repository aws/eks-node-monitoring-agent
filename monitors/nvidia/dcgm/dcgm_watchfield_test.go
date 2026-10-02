//go:build !darwin

package dcgm_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestFields(t *testing.T) {
	t.Run("FieldsError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{FieldErr: fmt.Errorf("error")}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.ErrorIs(t, err, mockDcgm.FieldErr)
		assert.Empty(t, conditions)
	})

	t.Run("IgnoreNotInitialized", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{FieldErr: dcgm.ErrNotInitialized}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("IgnoreHealthy", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{
			FieldValues: []dcgmapi.FieldValue_v2{
				{
					FieldID: dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
					Status:  dcgmapi.DCGM_ST_OK,
				},
			},
		}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("DropNonMappedFields", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{
			FieldValues: []dcgmapi.FieldValue_v2{
				{
					FieldID:   dcgmapi.DCGM_FI_GPU_TOPOLOGY_AFFINITY,
					Status:    dcgmapi.DCGM_ST_BADPARAM,
					FieldType: dcgmapi.DCGM_FT_STRING,
				},
			},
		}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	// The two FabricManagerStatus cases below cover the read + routing path through
	// WatchFields (FieldID recognition and reading the status from the raw value
	// bytes) for a fault and a healthy status. The decode of every
	// dcgmFabricManagerStatus_t value is covered by
	// TestClassify_fabricManagerStatusSpec (classify_fabric_spec_test.go).
	t.Run("FabricManagerStatusFailure", func(t *testing.T) {
		fieldValue := dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_FABRIC_MANAGER_STATUS}
		fieldValue.Status = dcgmapi.DCGM_ST_OK
		binary.LittleEndian.PutUint64(fieldValue.Value[:], 4) // DcgmFMStatusFailure
		mockDcgm := &fake.FakeDcgm{FieldValues: []dcgmapi.FieldValue_v2{fieldValue}}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.ElementsMatch(t, []monitor.Condition{{
			Reason:   "FabricManagerNotRunning",
			Message:  "Fabric Manager status: Failure",
			Severity: monitor.SeverityFatal,
		}}, conditions)
	})

	t.Run("FabricManagerStatusNotStarted", func(t *testing.T) {
		fieldValue := dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_FABRIC_MANAGER_STATUS}
		fieldValue.Status = dcgmapi.DCGM_ST_OK
		binary.LittleEndian.PutUint64(fieldValue.Value[:], 1) // DcgmFMStatusNotStarted
		mockDcgm := &fake.FakeDcgm{FieldValues: []dcgmapi.FieldValue_v2{fieldValue}}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	// A mask with a sub-field in the True/fault state is flagged, and the
	// message names the faulting sub-field. This case covers the DCGM FieldValue
	// read + routing path (FieldID recognition and reading the mask from the raw
	// value bytes end-to-end through WatchFields), which the Classify table test
	// does not exercise.
	t.Run("FabricHealthMaskFault", func(t *testing.T) {
		fieldValue := dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_FABRIC_HEALTH_MASK}
		fieldValue.Status = dcgmapi.DCGM_ST_OK
		// route_unhealthy=True (value 1 at shift 4) is a genuine fault.
		binary.LittleEndian.PutUint64(fieldValue.Value[:], 0x10)
		mockDcgm := &fake.FakeDcgm{FieldValues: []dcgmapi.FieldValue_v2{fieldValue}}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.ElementsMatch(t, []monitor.Condition{{
			Reason:   "NvidiaFabricError",
			Message:  "GPU fabric health mask 0x10: route_unhealthy=1",
			Severity: monitor.SeverityFatal,
		}}, conditions)
	})

	// Regression test: a mask of 0x80 decodes to access_timeout_recovery=False
	// (value 2 at shift 6) with every other sub-field NotSupported. That is a
	// fully healthy GPU, but the old "any non-zero mask is a fault" check
	// flagged it, causing the false-positive node disruptions this fix prevents.
	// This is the only case that exercises the healthy-mask plumbing end-to-end
	// (the mask branch returning no condition through WatchFields); the Classify
	// table test covers the 0x80 decode itself.
	t.Run("FabricHealthMaskFalseStateHealthy", func(t *testing.T) {
		fieldValue := dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_FABRIC_HEALTH_MASK}
		fieldValue.Status = dcgmapi.DCGM_ST_OK
		binary.LittleEndian.PutUint64(fieldValue.Value[:], 0x80) // access_timeout_recovery=False
		mockDcgm := &fake.FakeDcgm{FieldValues: []dcgmapi.FieldValue_v2{fieldValue}}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	// DCGM stores a blank value (DCGM_FT_INT64_BLANK, with status OK) when it
	// could not read a fabric field. This covers reading the blank from the raw
	// value bytes through WatchFields for both fabric fields; the Classify spec
	// tables cover every blank value.
	t.Run("FabricFieldsBlank", func(t *testing.T) {
		var fieldValues []dcgmapi.FieldValue_v2
		for _, id := range []dcgmapi.Short{dcgmapi.DCGM_FI_DEV_FABRIC_MANAGER_STATUS, dcgmapi.DCGM_FI_DEV_FABRIC_HEALTH_MASK} {
			fieldValue := dcgmapi.FieldValue_v2{FieldID: id, Status: dcgmapi.DCGM_ST_OK}
			binary.LittleEndian.PutUint64(fieldValue.Value[:], uint64(dcgmapi.DCGM_FT_INT64_BLANK))
			fieldValues = append(fieldValues, fieldValue)
		}
		mockDcgm := &fake.FakeDcgm{FieldValues: fieldValues}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.WatchFields(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	t.Run("GetResultForBadStatus", func(t *testing.T) {
		for _, test := range []struct {
			fieldValue      dcgmapi.FieldValue_v2
			value           int64
			expectedMessage string
		}{
			// Clock-throttle messages are covered by TestFieldsClockThrottleReasons.
			// Odd and large SXID codes are included on purpose: they are values a
			// naive encoding of the field value would not round-trip.
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS}, 2,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Fatal Error Code 2`, dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS}, 3,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Fatal Error Code 3`, dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS}, 1000,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Fatal Error Code 1000`, dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS}, 2,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Non-Fatal Error Code 2`, dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS}, 3,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Non-Fatal Error Code 3`, dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS}, 1000,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Non-Fatal Error Code 1000`, dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS),
			},
		} {
			t.Run(fmt.Sprintf("FI_%d/%d", test.fieldValue.FieldID, test.value), func(t *testing.T) {
				fieldValue := test.fieldValue
				// force the issue to be picked up with a bad status
				fieldValue.Status = dcgmapi.DCGM_ST_BADPARAM
				// embed the value the way FieldValue_v2.Int64() reads it: the
				// first 8 bytes of Value as a little-endian int64.
				binary.LittleEndian.PutUint64(fieldValue.Value[:], uint64(test.value))
				mockDcgm := &fake.FakeDcgm{
					FieldValues: []dcgmapi.FieldValue_v2{fieldValue},
				}
				dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
				conditions, err := dcgmSystem.WatchFields(context.TODO())
				assert.NoError(t, err)
				assert.ElementsMatch(t, []monitor.Condition{{
					Reason:   fmt.Sprintf("DCGMFieldError%d", mockDcgm.FieldValues[0].FieldID),
					Message:  test.expectedMessage,
					Severity: monitor.SeverityWarning,
				}}, conditions)
			})
		}
	})
}

// TestFieldsClockThrottleReasons pins the message for the clock-throttle field
// (DCGM_FI_DEV_CLOCKS_EVENT_REASONS) when DCGM reports a bad read status. Each
// defined reason bit on its own is named exactly; a mask with no defined bit set
// carries no reason suffix. The masks are the raw bit values from DCGM's
// dcgm_fields.h (named DCGM_CLOCKS_EVENT_REASON_* in current headers) rather
// than NMA's dcgm.DCGM_CLOCKS_THROTTLE_REASON_* constants named in each row, so
// the test also checks that those constants match DCGM.
//
// Known issue: with more than one reason bit set, the reported reason is chosen
// by iterating a Go map, so it varies between runs for the same mask. This test
// only asserts what holds today (the message names one of the set reasons). A
// fix would either always report the same reason for a given mask, or list every
// set reason; both change the message for multi-bit masks.
func TestFieldsClockThrottleReasons(t *testing.T) {
	const prefix = "DCGM detected fieldID 112 with statusCode -1"
	watch := func(t *testing.T, mask uint64) []monitor.Condition {
		t.Helper()
		fieldValue := dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, Status: dcgmapi.DCGM_ST_BADPARAM}
		binary.LittleEndian.PutUint64(fieldValue.Value[:], mask)
		mockDcgm := &fake.FakeDcgm{FieldValues: []dcgmapi.FieldValue_v2{fieldValue}}
		conditions, err := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType()).WatchFields(context.TODO())
		assert.NoError(t, err)
		return conditions
	}
	condition := func(message string) []monitor.Condition {
		return []monitor.Condition{{Reason: "DCGMFieldError112", Message: message, Severity: monitor.SeverityWarning}}
	}

	for _, tc := range []struct {
		mask   uint64
		reason string // "" => no reason suffix
	}{
		{0x0, ""},                 // no reason bit set
		{0x1, "gpu_idle"},         // DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE
		{0x2, "clocks_setting"},   // DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING
		{0x4, "sw_power_cap"},     // DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP
		{0x8, "hw_slowdown"},      // DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN
		{0x10, "sync_boost"},      // DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST
		{0x20, "sw_thermal"},      // DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL
		{0x40, "hw_thermal"},      // DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL
		{0x80, "hw_power_brake"},  // DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE
		{0x100, "display_clocks"}, // DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS
		{0x200, ""},               // not a defined reason bit
	} {
		t.Run(fmt.Sprintf("mask 0x%x", tc.mask), func(t *testing.T) {
			want := prefix
			if tc.reason != "" {
				want += fmt.Sprintf(": Clocks Throttle Reason %q", tc.reason)
			}
			assert.Equal(t, condition(want), watch(t, tc.mask))
		})
	}

	t.Run("multiple reasons names one of them", func(t *testing.T) {
		// DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN (0x8) + DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL
		// (0x20); which one is named varies today.
		got := watch(t, 0x28)
		assert.Contains(t, []any{
			condition(prefix + `: Clocks Throttle Reason "hw_slowdown"`),
			condition(prefix + `: Clocks Throttle Reason "sw_thermal"`),
		}, any(got))
	})
}
