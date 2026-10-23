package instanceinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadEmbeddedInstanceInfo(t *testing.T) {
	lookup := loadEmbeddedInstanceInfo()
	assert.NotEmpty(t, lookup, "embedded instance info should not be empty")

	tests := []struct {
		instanceType      string
		expectedGPUs      uint
		hostFabricManager bool
	}{
		{"g6e.12xlarge", 4, false},
		{"g6e.48xlarge", 8, false},
		{"g5.2xlarge", 1, false},
		{"p5.48xlarge", 8, true},
		{"p5.4xlarge", 1, false},
		{"p4d.24xlarge", 8, false},
		{"p6-b200.48xlarge", 8, true},
		{"p6e-gb200.36xlarge", 4, false},
		{"g4dn.metal", 8, false},
	}

	for _, tt := range tests {
		t.Run(tt.instanceType, func(t *testing.T) {
			info, ok := lookup[tt.instanceType]
			assert.True(t, ok, "instance type %s should be in embedded data", tt.instanceType)
			assert.Equal(t, tt.expectedGPUs, info.NvidiaGPUCount)
			assert.Equal(t, tt.hostFabricManager, info.HostFabricManager)
			assert.Equal(t, tt.instanceType, info.InstanceType)
		})
	}
}

func TestLoadEmbeddedInstanceInfoMissingType(t *testing.T) {
	lookup := loadEmbeddedInstanceInfo()
	_, ok := lookup["m5.xlarge"]
	assert.False(t, ok, "non-GPU instance type should not be in embedded data")
}
