//go:build !darwin

package dcgm

import (
	"errors"
	"testing"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyReadNeedsDefaultSetup(t *testing.T) {
	assert.True(t, policyReadNeedsDefaultSetup(&dcgmapi.Error{Code: dcgmapi.DCGM_ST_NOT_CONFIGURED}))
	assert.True(t, policyReadNeedsDefaultSetup(&dcgmapi.Error{Code: dcgmapi.DCGM_ST_INSUFFICIENT_SIZE}))
	assert.False(t, policyReadNeedsDefaultSetup(errors.New("connection failed")))
}

func TestPolicyConfigsUsesConfiguredPowerThreshold(t *testing.T) {
	status := &dcgmapi.PolicyStatus{
		Action:     dcgmapi.PolicyActionGPUReset,
		Validation: dcgmapi.PolicyValidationShort,
		Conditions: map[dcgmapi.PolicyCondition]interface{}{
			dcgmapi.DbePolicy:     true,
			dcgmapi.XidPolicy:     true,
			dcgmapi.NvlinkPolicy:  true,
			dcgmapi.MaxRtPgPolicy: uint32(12),
			dcgmapi.PowerPolicy:   uint32(250),
			dcgmapi.ThermalPolicy: uint32(85),
			dcgmapi.PCIePolicy:    true,
		},
	}
	configs, err := policyConfigs(logr.Discard(), status, 1000)
	require.NoError(t, err)
	require.Len(t, configs, len(monitoredPolicyConditions))

	for index, config := range configs {
		assert.Equal(t, monitoredPolicyConditions[index], config.Condition)
		switch config.Condition {
		case dcgmapi.MaxRtPgPolicy:
			require.NotNil(t, config.MaxRetiredPages)
			assert.Equal(t, uint32(12), *config.MaxRetiredPages)
		case dcgmapi.PowerPolicy:
			require.NotNil(t, config.MaxPower)
			assert.Equal(t, uint32(1000), *config.MaxPower)
		case dcgmapi.ThermalPolicy:
			require.NotNil(t, config.MaxTemperature)
			assert.Equal(t, uint32(85), *config.MaxTemperature)
		}
	}
	require.NotNil(t, configs[0].Action)
	assert.Equal(t, dcgmapi.PolicyActionGPUReset, *configs[0].Action)
	require.NotNil(t, configs[0].Validation)
	assert.Equal(t, dcgmapi.PolicyValidationShort, *configs[0].Validation)
}

func TestPolicyConfigsAddsMissingDefaultsToExistingPartialPolicy(t *testing.T) {
	status := &dcgmapi.PolicyStatus{
		Conditions: map[dcgmapi.PolicyCondition]interface{}{
			dcgmapi.ThermalPolicy: uint32(85),
		},
	}
	configs, err := policyConfigs(logr.Discard(), status, 1000)
	require.NoError(t, err)
	require.Len(t, configs, len(monitoredPolicyConditions))

	for _, config := range configs {
		switch config.Condition {
		case dcgmapi.MaxRtPgPolicy:
			require.NotNil(t, config.MaxRetiredPages)
			assert.Equal(t, uint32(dcgmapi.DefaultMaxRetiredPages), *config.MaxRetiredPages)
		case dcgmapi.PowerPolicy:
			require.NotNil(t, config.MaxPower)
			assert.Equal(t, uint32(1000), *config.MaxPower)
		case dcgmapi.ThermalPolicy:
			require.NotNil(t, config.MaxTemperature)
			assert.Equal(t, uint32(85), *config.MaxTemperature)
		}
	}
}

func TestPolicyThresholdUint32(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  uint32
	}{
		{name: "Uint32", value: uint32(85), want: 85},
		{name: "Uint", value: uint(85), want: 85},
		{name: "Int", value: int(85), want: 85},
		{name: "Int32", value: int32(85), want: 85},
		{name: "Int64", value: int64(85), want: 85},
		{name: "Negative", value: int64(-1), want: dcgmapi.DefaultMaxTemperature},
		{name: "Unsupported", value: "85", want: dcgmapi.DefaultMaxTemperature},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := policyThresholdUint32(logr.Discard(), dcgmapi.ThermalPolicy, test.value, dcgmapi.DefaultMaxTemperature)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestPolicyConfigsUsesDefaultsForUnsupportedThresholds(t *testing.T) {
	status := &dcgmapi.PolicyStatus{
		Conditions: map[dcgmapi.PolicyCondition]interface{}{
			dcgmapi.MaxRtPgPolicy: "12",
			dcgmapi.ThermalPolicy: int64(-1),
		},
	}
	configs, err := policyConfigs(logr.Discard(), status, 1000)
	require.NoError(t, err)

	for _, config := range configs {
		switch config.Condition {
		case dcgmapi.MaxRtPgPolicy:
			require.NotNil(t, config.MaxRetiredPages)
			assert.Equal(t, uint32(dcgmapi.DefaultMaxRetiredPages), *config.MaxRetiredPages)
		case dcgmapi.ThermalPolicy:
			require.NotNil(t, config.MaxTemperature)
			assert.Equal(t, uint32(dcgmapi.DefaultMaxTemperature), *config.MaxTemperature)
		}
	}
}
