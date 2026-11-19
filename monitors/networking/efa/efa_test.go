package efa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// setupPCIeDevice lays out a PCI device the way sysfs does: the device
// directory sits under the directory of the PCIe port above it, and
// /sys/bus/pci/devices/<address> links to it. Each link is a speed and width
// pair, for example {"32.0 GT/s PCIe", "16"}; an empty pair leaves the files
// out.
func setupPCIeDevice(t *testing.T, address, vendorId, deviceId string, current, maximum, portMax [2]string) {
	root := config.HostRoot()
	portDir := filepath.Join(root, "/sys/devices/pci0000:00/0000:00:01.0")
	deviceDir := filepath.Join(portDir, address)
	require.NoError(t, os.MkdirAll(deviceDir, 0755))
	writeLink := func(dir, prefix string, link [2]string) {
		if link == [2]string{} {
			return
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+"_link_speed"), []byte(link[0]+"\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+"_link_width"), []byte(link[1]+"\n"), 0644))
	}
	writeLink(portDir, "max", portMax)
	require.NoError(t, os.WriteFile(filepath.Join(deviceDir, "vendor"), []byte(vendorId+"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(deviceDir, "device"), []byte(deviceId+"\n"), 0644))
	writeLink(deviceDir, "current", current)
	writeLink(deviceDir, "max", maximum)
	busDir := filepath.Join(root, "/sys/bus/pci/devices")
	require.NoError(t, os.MkdirAll(busDir, 0755))
	require.NoError(t, os.Symlink(filepath.Join("../../../devices/pci0000:00/0000:00:01.0", address), filepath.Join(busDir, address)))
}

func TestEFAPCIeLinks(t *testing.T) {
	gen5x16 := [2]string{"32.0 GT/s PCIe", "16"}
	gen5x8 := [2]string{"32.0 GT/s PCIe", "8"}
	gen4x16 := [2]string{"16.0 GT/s PCIe", "16"}

	t.Run("NoPCIDevices", func(t *testing.T) {
		SetupRoot(t)
		links, err := EFAPCIeLinks()
		assert.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("HealthyLink", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, gen5x16, gen5x16, gen5x16)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.Equal(t, "0000:31:00.0", links[0].Address)
		assert.False(t, links[0].Degraded())
	})

	t.Run("DegradedWidth", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, gen5x8, gen5x16, gen5x16)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.True(t, links[0].Degraded())
		assert.Equal(t, "x8 at 32 GT/s", links[0].Current.String())
		assert.Equal(t, "x16 at 32 GT/s", links[0].Capable.String())
	})

	t.Run("DegradedSpeed", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, gen4x16, gen5x16, gen5x16)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.True(t, links[0].Degraded())
	})

	t.Run("LimitedByConnectedPortIsNotDegraded", func(t *testing.T) {
		SetupRoot(t)
		// a Gen5 device in a Gen4 slot trains to Gen4, which is all it can get.
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, gen4x16, gen5x16, gen4x16)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.False(t, links[0].Degraded())
		assert.Equal(t, PCIeLink{SpeedGTs: 16, Width: 16}, links[0].Capable)
	})

	t.Run("OlderKernelSpeedFormat", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, [2]string{"8 GT/s", "16"}, [2]string{"8 GT/s", "16"}, [2]string{})
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.Equal(t, PCIeLink{SpeedGTs: 8, Width: 16}, links[0].Current)
		assert.False(t, links[0].Degraded())
	})

	t.Run("SkipsUnknownOrMissingLinkState", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:31:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, [2]string{"Unknown", "16"}, gen5x16, gen5x16)
		setupPCIeDevice(t, "0000:32:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, [2]string{"32.0 GT/s PCIe", "0"}, gen5x16, gen5x16)
		setupPCIeDevice(t, "0000:33:00.0", EFA_VENDOR_ID, EFA_DEVICE_ID, [2]string{}, gen5x16, gen5x16)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		assert.Empty(t, links, "devices without a known link state should be skipped")
	})

	t.Run("SkipsNonEFADevices", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeDevice(t, "0000:05:00.0", EFA_VENDOR_ID, "0xec20", gen5x8, gen5x16, gen5x16) // ENA
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		assert.Empty(t, links)
	})
}

// pcieNode is one device in a fake sysfs PCI device tree.
type pcieNode struct {
	// path is the device's directory under /sys/devices, for example
	// "pci0000:7a/0000:7a:01.0/0000:7b:00.0". Its parent directory is the
	// port above it.
	path             string
	vendor, device   string
	current, maximum [2]string
}

// setupPCIeTree writes nodes under /sys/devices and links each one from
// /sys/bus/pci/devices, the way the kernel lays out sysfs. Link files are
// left out for an empty speed and width pair.
func setupPCIeTree(t *testing.T, nodes ...pcieNode) {
	root := config.HostRoot()
	busDir := filepath.Join(root, "/sys/bus/pci/devices")
	require.NoError(t, os.MkdirAll(busDir, 0755))
	for _, node := range nodes {
		dir := filepath.Join(root, "/sys/devices", node.path)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor"), []byte(node.vendor+"\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "device"), []byte(node.device+"\n"), 0644))
		for prefix, link := range map[string][2]string{"current": node.current, "max": node.maximum} {
			if link == [2]string{} {
				continue
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+"_link_speed"), []byte(link[0]+"\n"), 0644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+"_link_width"), []byte(link[1]+"\n"), 0644))
		}
		require.NoError(t, os.Symlink(filepath.Join("../../../devices", node.path), filepath.Join(busDir, filepath.Base(node.path))))
	}
}

// TestEFAPCIeLinksRealTopologies lays out the PCI device trees read on real
// instances, to check which port limits the device against them.
func TestEFAPCIeLinksRealTopologies(t *testing.T) {
	const (
		amazonVendor = "0x1d0f"
		intelVendor  = "0x8086"
		nvidiaVendor = "0x10de"
		switchDevice = "0xbec2" // AWS PCIe switch port
		enaDevice    = "0xec20"
	)
	gen3x16 := [2]string{"8.0 GT/s PCIe", "16"}
	gen3x8 := [2]string{"8.0 GT/s PCIe", "8"}
	unknown := [2]string{"Unknown", "0"}
	unknownMax := [2]string{"Unknown", "255"}

	// m5zn.metal: the EFA device sits behind an AWS PCIe switch, whose upstream
	// port hangs off an Intel root port. Unused switch ports report an unknown
	// current link but a known maximum, and the ENA device shares the switch.
	m5znMetal := func(efaCurrent, upstreamMax [2]string) []pcieNode {
		const rootPort = "pci0000:7a/0000:7a:00.0"
		const upstream = rootPort + "/0000:7b:00.0"
		return []pcieNode{
			{path: rootPort, vendor: intelVendor, device: "0x347a", current: gen3x16, maximum: gen3x16},
			{path: upstream, vendor: amazonVendor, device: switchDevice, current: gen3x16, maximum: upstreamMax},
			{path: upstream + "/0000:7c:00.0", vendor: amazonVendor, device: switchDevice, current: gen3x16, maximum: gen3x16},
			{path: upstream + "/0000:7c:01.0", vendor: amazonVendor, device: switchDevice, current: gen3x16, maximum: gen3x16},
			{path: upstream + "/0000:7c:02.0", vendor: amazonVendor, device: switchDevice, current: unknown, maximum: gen3x16},
			{path: upstream + "/0000:7c:00.0/0000:7d:00.0", vendor: amazonVendor, device: EFA_DEVICE_ID, current: efaCurrent, maximum: gen3x16},
			{path: upstream + "/0000:7c:01.0/0000:7e:00.0", vendor: amazonVendor, device: enaDevice, current: gen3x8, maximum: gen3x16},
		}
	}

	t.Run("MetalHealthy", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeTree(t, m5znMetal(gen3x16, gen3x16)...)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1, "only the EFA device should be returned, not the switch ports or ENA")
		assert.Equal(t, "0000:7d:00.0", links[0].Address)
		assert.Equal(t, PCIeLink{SpeedGTs: 8, Width: 16}, links[0].Capable)
		assert.False(t, links[0].Degraded())
	})

	t.Run("MetalLostLanes", func(t *testing.T) {
		SetupRoot(t)
		setupPCIeTree(t, m5znMetal(gen3x8, gen3x16)...)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.True(t, links[0].Degraded())
		assert.Equal(t, "x8 at 8 GT/s", links[0].Current.String())
		assert.Equal(t, "x16 at 8 GT/s", links[0].Capable.String())
	})

	t.Run("MetalPortsFurtherUpDoNotHideDowntrain", func(t *testing.T) {
		SetupRoot(t)
		// the switch's upstream port, two levels above the device, only has
		// 8 lanes. That is a separate link: the device's own link to its x16
		// switch port should still train to x16, so x8 is degraded.
		setupPCIeTree(t, m5znMetal(gen3x8, gen3x8)...)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.Equal(t, PCIeLink{SpeedGTs: 8, Width: 16}, links[0].Capable)
		assert.True(t, links[0].Degraded())
	})

	t.Run("P6eGB200", func(t *testing.T) {
		SetupRoot(t)
		// the devices sit directly on a root bus with no port above them. The
		// GPUs train below their own maximum, which must not affect EFA.
		gen5x16 := [2]string{"32.0 GT/s PCIe", "16"}
		setupPCIeTree(t,
			pcieNode{path: "pci0000:04/0000:04:00.0", vendor: amazonVendor, device: enaDevice, current: [2]string{"32.0 GT/s PCIe", "8"}, maximum: [2]string{"32.0 GT/s PCIe", "8"}},
			pcieNode{path: "pci0000:28/0000:28:00.0", vendor: amazonVendor, device: "0xefa3", current: gen5x16, maximum: gen5x16},
			pcieNode{path: "pci0000:29/0000:29:00.0", vendor: nvidiaVendor, device: "0x2941", current: gen5x16, maximum: [2]string{"64.0 GT/s PCIe", "16"}},
		)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		require.Len(t, links, 1, "the GPU and ENA devices should not be returned")
		assert.Equal(t, "0000:28:00.0", links[0].Address)
		assert.Equal(t, PCIeLink{SpeedGTs: 32, Width: 16}, links[0].Capable)
		assert.False(t, links[0].Degraded())
	})

	t.Run("VirtualInstanceWithoutLinkState", func(t *testing.T) {
		SetupRoot(t)
		// c5n.9xlarge and m6i.32xlarge report the link as unknown.
		setupPCIeTree(t,
			pcieNode{path: "pci0000:00/0000:00:05.0", vendor: amazonVendor, device: enaDevice, current: unknown, maximum: unknownMax},
			pcieNode{path: "pci0000:00/0000:00:1f.0", vendor: amazonVendor, device: "0xefa1", current: unknown, maximum: unknownMax},
		)
		links, err := EFAPCIeLinks()
		require.NoError(t, err)
		assert.Empty(t, links, "devices that do not report their link should be skipped")
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
