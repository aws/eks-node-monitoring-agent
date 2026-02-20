package ebsnvme

import (
	"testing"
)

func TestNvmeIoctlWithInvalidDevice(t *testing.T) {
	// Create a device with an invalid path
	invalidPaths := []string{
		"/dev/nonexistent",
		"",
		"/tmp/not-a-device",
	}

	for _, invalidPath := range invalidPaths {
		t.Run("Invalid path: "+invalidPath, func(t *testing.T) {
			device := NewDevice(invalidPath)

			// Create a dummy admin command
			adminCmd := &NvmeAdminCommand{}

			// Attempt to perform an ioctl operation
			err := device.NvmeIoctl(adminCmd)

			// Verify that an error is returned
			if err == nil {
				t.Errorf("Expected error when using invalid device path %q, but got nil", invalidPath)
			}
		})
	}
}

func TestByteToString(t *testing.T) {
	testCases := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "String with null terminators",
			input:    []byte{'t', 'e', 's', 't', 0, 0, 0},
			expected: "test",
		},
		{
			name:     "String without null terminators",
			input:    []byte{'t', 'e', 's', 't'},
			expected: "test",
		},
		{
			name:     "Empty string",
			input:    []byte{},
			expected: "",
		},
		{
			name:     "Only null terminators",
			input:    []byte{0, 0, 0},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := BytesToString(tc.input)
			if result != tc.expected {
				t.Errorf("Expected %q but got %q", tc.expected, result)
			}
		})
	}
}
