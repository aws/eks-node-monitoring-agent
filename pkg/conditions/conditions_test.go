package conditions

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestConditionTypes(t *testing.T) {
	tests := []struct {
		name          string
		conditionType corev1.NodeConditionType
		expectedValue string
	}{
		{
			name:          "AcceleratedHardwareReady",
			conditionType: AcceleratedHardwareReady,
			expectedValue: "AcceleratedHardwareReady",
		},
		{
			name:          "ContainerRuntimeReady",
			conditionType: ContainerRuntimeReady,
			expectedValue: "ContainerRuntimeReady",
		},
		{
			name:          "DiskPressure",
			conditionType: DiskPressure,
			expectedValue: "DiskPressure",
		},
		{
			name:          "KernelReady",
			conditionType: KernelReady,
			expectedValue: "KernelReady",
		},
		{
			name:          "MemoryPressure",
			conditionType: MemoryPressure,
			expectedValue: "MemoryPressure",
		},
		{
			name:          "NetworkingReady",
			conditionType: NetworkingReady,
			expectedValue: "NetworkingReady",
		},
		{
			name:          "Ready",
			conditionType: Ready,
			expectedValue: "Ready",
		},
		{
			name:          "StorageReady",
			conditionType: StorageReady,
			expectedValue: "StorageReady",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.conditionType) != tt.expectedValue {
				t.Errorf("condition type %s = %v, want %v", tt.name, tt.conditionType, tt.expectedValue)
			}
		})
	}
}

// TestConditionTypesAreUnique ensures all condition types have unique values
func TestConditionTypesAreUnique(t *testing.T) {
	conditions := []corev1.NodeConditionType{
		AcceleratedHardwareReady,
		ContainerRuntimeReady,
		DiskPressure,
		KernelReady,
		MemoryPressure,
		NetworkingReady,
		Ready,
		StorageReady,
	}

	seen := make(map[corev1.NodeConditionType]bool)
	for _, cond := range conditions {
		if seen[cond] {
			t.Errorf("duplicate condition type found: %s", cond)
		}
		seen[cond] = true
	}

	if len(seen) != 8 {
		t.Errorf("expected 8 unique condition types, got %d", len(seen))
	}
}
