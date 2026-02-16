//go:build !darwin

package dcgm_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
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

	t.Run("GetResultForBadStatus", func(t *testing.T) {
		for _, test := range []struct {
			fieldValue      dcgmapi.FieldValue_v2
			value           int64
			expectedMessage string
		}{
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS}, dcgm.DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: Clocks Throttle Reason "clocks_setting"`, dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS}, 2,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Fatal Error Code 2`, dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS),
			},
			{
				dcgmapi.FieldValue_v2{FieldID: dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS}, 2,
				fmt.Sprintf(`DCGM detected fieldID %d with statusCode -1: SXID Non-Fatal Error Code 2`, dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS),
			},
		} {
			t.Run(fmt.Sprintf("FI_%d", test.fieldValue.FieldID), func(t *testing.T) {
				fieldValue := test.fieldValue
				// force the issue to be picked up with a bad status
				fieldValue.Status = dcgmapi.DCGM_ST_BADPARAM
				// embed the int representation of the value into the field
				// TODO: temporary hack because the transformation on uint64 in
				// PutVarint. we should verify whether this is accurate
				// behavior at runtime, but not blocking.
				binary.PutVarint(fieldValue.Value[:], test.value>>1)
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
