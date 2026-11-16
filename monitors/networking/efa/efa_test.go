package efa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

const EFA_DEVICE_ID = "0xefa0"

func TestEFASystem(t *testing.T) {
	t.Run("NoDevices", func(t *testing.T) {
		SetupRoot(t)

		efaSystem := NewEFASystem()
		conditions, err := efaSystem.HardwareCounters(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("NoEFADevice", func(t *testing.T) {
		SetupRoot(t)
		SetupDevice(t, "foo", 1, "bar", "baz")

		efaSystem := NewEFASystem()
		conditions, err := efaSystem.HardwareCounters(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("NotEFAVendor", func(t *testing.T) {
		SetupRoot(t)
		SetupDevice(t, "foo", 1, "foo", EFA_DEVICE_ID)

		efaSystem := NewEFASystem()
		conditions, err := efaSystem.HardwareCounters(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("NotEFADevice", func(t *testing.T) {
		SetupRoot(t)
		SetupDevice(t, "foo", 1, EFA_VENDOR_ID, "bar")

		efaSystem := NewEFASystem()
		conditions, err := efaSystem.HardwareCounters(context.TODO())
		assert.NoError(t, err)
		assert.Len(t, conditions, 0)
	})

	t.Run("EFADeviceCounter", func(t *testing.T) {
		for metricName := range NewEFASystem().counterConstructors {
			t.Run(metricName, func(t *testing.T) {
				SetupRoot(t)
				SetupDevice(t, "foo", 1, EFA_VENDOR_ID, EFA_DEVICE_ID)
				SetupDeviceCounter(t, "foo", 1, metricName, 1_000_000_000)

				efaSystem := NewEFASystem()
				conditions, err := efaSystem.HardwareCounters(context.TODO())
				assert.NoError(t, err)
				assert.Len(t, conditions, 1)
				assert.Equal(t, conditions[0], monitor.Condition{
					Reason:   "EFAErrorMetric",
					Message:  fmt.Sprintf("%s increased from %d to %d for device:port [%s:%d]", metricName, 0, 1_000_000_000, "foo", 1),
					Severity: monitor.SeverityWarning,
				})
			})
		}
	})
}

func TestCountActiveEFADevices(t *testing.T) {
	t.Run("NoInfinibandClass", func(t *testing.T) {
		SetupRoot(t)

		count, err := CountActiveEFADevices()
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("MixedDevices", func(t *testing.T) {
		SetupRoot(t)
		for device, state := range map[string][2]string{
			"efa0":        {"4: ACTIVE", "5: LinkUp"},
			"efa1":        {"4: ACTIVE", "5: LinkUp"},
			"efadown":     {"1: DOWN", "2: Polling"},
			"efanolink":   {"4: ACTIVE", "3: Disabled"},
			"efainactive": {"2: INIT", "5: LinkUp"},
		} {
			SetupDevice(t, device, 1, EFA_VENDOR_ID, EFA_DEVICE_ID)
			SetupPortState(t, device, 1, state[0], state[1])
		}
		SetupDevice(t, "efanostate", 1, EFA_VENDOR_ID, EFA_DEVICE_ID)
		SetupDevice(t, "mlx0", 1, "0x15b3", "0x101b")
		SetupPortState(t, "mlx0", 1, "4: ACTIVE", "5: LinkUp")
		SetupDevice(t, "notefa", 1, EFA_VENDOR_ID, "bar")
		SetupPortState(t, "notefa", 1, "4: ACTIVE", "5: LinkUp")

		count, err := CountActiveEFADevices()
		assert.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestCountEFAPCIDevices(t *testing.T) {
	t.Run("NoPCIDevices", func(t *testing.T) {
		SetupRoot(t)

		devices, err := CountEFAPCIDevices()
		assert.NoError(t, err)
		assert.Equal(t, EFAPCIDevices{}, devices)
	})

	t.Run("MixedDevices", func(t *testing.T) {
		SetupRoot(t)
		SetupPCIDevice(t, "0000:00:06.0", EFA_VENDOR_ID, EFA_DEVICE_ID, "efa")
		SetupPCIDevice(t, "0000:00:07.0", EFA_VENDOR_ID, "0xefa1", "efa")
		SetupPCIDevice(t, "0000:00:08.0", EFA_VENDOR_ID, EFA_DEVICE_ID, "")
		SetupPCIDevice(t, "0000:00:09.0", EFA_VENDOR_ID, EFA_DEVICE_ID, "vfio-pci")
		SetupPCIDevice(t, "0000:00:05.0", EFA_VENDOR_ID, "0xec20", "ena")
		SetupPCIDevice(t, "0000:00:0a.0", "0x15b3", "0x101b", "mlx5_core")

		devices, err := CountEFAPCIDevices()
		assert.NoError(t, err)
		assert.Equal(t, EFAPCIDevices{Attached: 4, Bound: 2}, devices)
	})
}

func SetupRoot(t *testing.T) string {
	root := t.TempDir()
	t.Setenv(config.HOST_ROOT_ENV, root)
	return root
}

func SetupDevice(t *testing.T, device string, port int, vendorId, deviceId string) {
	root := config.HostRoot()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/ports/%d/hw_counters/", device, port)), 0755))
	assert.NoError(t, os.MkdirAll(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/device/", device)), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/device/vendor", device)), []byte(vendorId+"\n"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/device/device", device)), []byte(deviceId+"\n"), 0755))
}

// SetupPortState writes the logical and physical state of an infiniband port.
func SetupPortState(t *testing.T, device string, port int, state, physState string) {
	portDir := filepath.Join(config.HostRoot(), fmt.Sprintf("/sys/class/infiniband/%s/ports/%d", device, port))
	assert.NoError(t, os.WriteFile(filepath.Join(portDir, "state"), []byte(state+"\n"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(portDir, "phys_state"), []byte(physState+"\n"), 0755))
}

// SetupPCIDevice creates a pci device at address, bound to driver unless
// driver is empty.
func SetupPCIDevice(t *testing.T, address, vendorId, deviceId, driver string) {
	root := config.HostRoot()
	deviceDir := filepath.Join(root, "/sys/bus/pci/devices", address)
	assert.NoError(t, os.MkdirAll(deviceDir, 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(deviceDir, "vendor"), []byte(vendorId+"\n"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(deviceDir, "device"), []byte(deviceId+"\n"), 0755))
	if driver != "" {
		assert.NoError(t, os.MkdirAll(filepath.Join(root, "/sys/bus/pci/drivers", driver), 0755))
		assert.NoError(t, os.Symlink(filepath.Join("../../drivers", driver), filepath.Join(deviceDir, "driver")))
	}
}

func SetupDeviceCounter(t *testing.T, device string, port int, counterName string, counterValue int) {
	root := config.HostRoot()
	assert.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/ports/%d/hw_counters/%s", device, port, counterName)), []byte(strconv.Itoa(counterValue)+"\n"), 0755))
}

func TestUtils(t *testing.T) {
	assert.Equal(t, -1, globIndex("", 0))
	assert.Equal(t, 0, globIndex("*", 0))
	assert.Equal(t, 2, globIndex("*/foo/*", 1))
}
