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

func SetupDeviceCounter(t *testing.T, device string, port int, counterName string, counterValue int) {
	root := config.HostRoot()
	assert.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("/sys/class/infiniband/%s/ports/%d/hw_counters/%s", device, port, counterName)), []byte(strconv.Itoa(counterValue)+"\n"), 0755))
}

func TestUtils(t *testing.T) {
	assert.Equal(t, -1, globIndex("", 0))
	assert.Equal(t, 0, globIndex("*", 0))
	assert.Equal(t, 2, globIndex("*/foo/*", 1))
}
