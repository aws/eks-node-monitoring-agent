//go:build !darwin

package dcgm_test

import (
	"context"
	"testing"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestPolicies(t *testing.T) {
	t.Run("UnhandledFinding", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{PolicyChan: make(chan dcgmapi.PolicyViolation, 1)}
		mockDcgm.PolicyChan <- dcgmapi.PolicyViolation{Condition: "mock"}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.Policies(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})

	for _, policy := range []dcgmapi.PolicyViolation{
		{
			Condition: dcgmapi.DbePolicy,
			Data:      dcgmapi.DbePolicyCondition{},
		},
		{
			Condition: dcgmapi.MaxRtPgPolicy,
			Data:      dcgmapi.RetiredPagesPolicyCondition{},
		},
		{
			Condition: dcgmapi.XidPolicy,
			Data: dcgmapi.XidPolicyCondition{
				ErrNum: 0,
			},
		},
		{
			Condition: dcgmapi.XidPolicy,
			Data: dcgmapi.XidPolicyCondition{
				ErrNum: dcgm.WellKnownXidCodes[0],
			},
		},
		{
			Condition: dcgmapi.NvlinkPolicy,
			Data:      dcgmapi.NvlinkPolicyCondition{},
		},
		{
			Condition: dcgmapi.ThermalPolicy,
			Data: dcgmapi.ThermalPolicyCondition{
				// should be non-zero to trigger
				ThermalViolation: 1,
			},
		},
		{
			Condition: dcgmapi.PowerPolicy,
			Data: dcgmapi.PowerPolicyCondition{
				// should be non-zero to trigger
				PowerViolation: 1,
			},
		},
		{
			Condition: dcgmapi.PCIePolicy,
			Data:      dcgmapi.PciPolicyCondition{},
		},
	} {
		t.Run("GetResult-"+string(policy.Condition), func(t *testing.T) {
			mockDcgm := &fake.FakeDcgm{PolicyChan: make(chan dcgmapi.PolicyViolation, 1)}
			mockDcgm.PolicyChan <- policy
			dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
			conditions, err := dcgmSystem.Policies(context.TODO())
			assert.NoError(t, err)
			assert.Len(t, conditions, 1)
		})
	}
}
