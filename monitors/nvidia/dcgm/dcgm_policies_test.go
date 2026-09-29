//go:build !darwin

package dcgm_test

import (
	"context"
	"testing"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestPolicies(t *testing.T) {
	// When a finding produces no condition, Policies returns a nil slice and no
	// error, for both an unrecognized finding and a power/thermal policy that
	// reports no violation. Callers only range over the result, but nil (rather
	// than an empty slice) is the established contract, so it is asserted exactly.
	t.Run("NoMatchReturnsNil", func(t *testing.T) {
		for name, policy := range map[string]dcgmapi.PolicyViolation{
			"UnhandledFinding":   {Condition: "mock"},
			"PowerNotViolated":   {Condition: dcgmapi.PowerPolicy, Data: dcgmapi.PowerPolicyCondition{PowerViolation: 0}},
			"ThermalNotViolated": {Condition: dcgmapi.ThermalPolicy, Data: dcgmapi.ThermalPolicyCondition{ThermalViolation: 0}},
		} {
			t.Run(name, func(t *testing.T) {
				mockDcgm := &fake.FakeDcgm{PolicyChan: make(chan dcgmapi.PolicyViolation, 1)}
				mockDcgm.PolicyChan <- policy
				dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
				conditions, err := dcgmSystem.Policies(context.TODO())
				assert.NoError(t, err)
				assert.Nil(t, conditions)
			})
		}
	})

	// Each policy is checked for its complete condition (reason, severity, and
	// message), end to end through Policies(). The messages are the exact
	// strings the handlers emit, so this guards both repair behavior and the
	// log/event text.
	//
	// Use distinct, non-zero values for every field of a finding. Zero values
	// would still check the message format, but not that each field reaches the
	// right placeholder: e.g. swapped SbePages/DbePages would go unnoticed with
	// both set to 0, while 4 and 5 make the swap fail the test.
	for _, tc := range []struct {
		name   string
		policy dcgmapi.PolicyViolation
		want   []monitor.Condition
	}{
		{
			name:   "WellKnownXid",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.XidPolicy, Data: dcgmapi.XidPolicyCondition{ErrNum: 79}},
			want: []monitor.Condition{{
				Reason:   "NvidiaXID79Error",
				Message:  "detected XID-79 on the instance, review kernel logs for additional information.",
				Severity: monitor.SeverityFatal,
			}},
		},
		{
			name:   "UnknownXid",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.XidPolicy, Data: dcgmapi.XidPolicyCondition{ErrNum: 13}},
			want: []monitor.Condition{{
				Reason:   "NvidiaXID13Warning",
				Message:  "detected unknown XID-13 on the instance, review kernel logs for additional information.",
				Severity: monitor.SeverityWarning,
			}},
		},
		{
			name:   "Dbe",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.DbePolicy, Data: dcgmapi.DbePolicyCondition{NumErrors: 2, Location: "DEVICE"}},
			want: []monitor.Condition{{
				Reason:   "NvidiaDoubleBitError",
				Message:  "detected 2 Nvidia Double Bit error(s) on location DEVICE",
				Severity: monitor.SeverityFatal,
			}},
		},
		{
			name:   "Nvlink",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.NvlinkPolicy, Data: dcgmapi.NvlinkPolicyCondition{Counter: 3, FieldId: 404}},
			want: []monitor.Condition{{
				Reason:   "NvidiaNVLinkError",
				Message:  "detected 3 NVLink errors on fieldId 404",
				Severity: monitor.SeverityFatal,
			}},
		},
		{
			name:   "PageRetirement",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.MaxRtPgPolicy, Data: dcgmapi.RetiredPagesPolicyCondition{SbePages: 4, DbePages: 5}},
			want: []monitor.Condition{{
				Reason:   "NvidiaPageRetirement",
				Message:  "detected 4 SBE, and 5 DBE page retirements",
				Severity: monitor.SeverityWarning,
			}},
		},
		{
			name:   "Power",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.PowerPolicy, Data: dcgmapi.PowerPolicyCondition{PowerViolation: 7}},
			want: []monitor.Condition{{
				Reason:   "NvidiaPowerError",
				Message:  "detected power usage outside of thresholds with severity code 7",
				Severity: monitor.SeverityWarning,
			}},
		},
		{
			name:   "PCIe",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.PCIePolicy, Data: dcgmapi.PciPolicyCondition{ReplayCounter: 6}},
			want: []monitor.Condition{{
				Reason:   "NvidiaPCIeError",
				Message:  "detected 6 PCIe replays",
				Severity: monitor.SeverityWarning,
			}},
		},
		{
			name:   "Thermal",
			policy: dcgmapi.PolicyViolation{Condition: dcgmapi.ThermalPolicy, Data: dcgmapi.ThermalPolicyCondition{ThermalViolation: 8}},
			want: []monitor.Condition{{
				Reason:   "NvidiaThermalError",
				Message:  "detected GPU thermals outside of thresholds with severity code 8",
				Severity: monitor.SeverityWarning,
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockDcgm := &fake.FakeDcgm{PolicyChan: make(chan dcgmapi.PolicyViolation, 1)}
			mockDcgm.PolicyChan <- tc.policy
			dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
			conditions, err := dcgmSystem.Policies(context.TODO())
			assert.NoError(t, err)
			assert.Equal(t, tc.want, conditions)
		})
	}
}
