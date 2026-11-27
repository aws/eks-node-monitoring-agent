package efa

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

const (
	EFA_VENDOR_ID        = "0x1d0f"
	EFA_DEVICE_ID_PREFIX = "0xefa"

	// https://www.kernel.org/doc/Documentation/ABI/stable/sysfs-class-infiniband
	infinibandSysfsPath = "/sys/class/infiniband"

	// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-bus-pci
	pciDevicesSysfsPath = "/sys/bus/pci/devices"
	efaDriverName       = "efa"
)

func NewEFASystem() *efaSystem {
	return &efaSystem{
		// see: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/efa-working-monitor.html#efa-driver-metrics
		counterConstructors: map[string]func(string, string) *deviceCounterTracker{
			"rx_drops": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
			"rdma_write_wr_err": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
			"rdma_read_wr_err": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
			"unresponsive_remote_events": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
			"impaired_remote_conn_events": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
			"retrans_bytes": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 1_000_000),
				}
			},
			"retrans_pkts": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 1_000),
				}
			},
			"retrans_timeout_events": func(deviceName, port string) *deviceCounterTracker {
				return &deviceCounterTracker{
					name:        deviceName,
					port:        port,
					rateLimiter: rate.NewLimiter(rate.Every(time.Second), 100),
				}
			},
		},
		counterTrackers: map[counterKey]*deviceCounterTracker{},
	}
}

type efaSystem struct {
	counterConstructors map[string]func(string, string) *deviceCounterTracker
	counterTrackers     map[counterKey]*deviceCounterTracker
}

type counterKey struct {
	deviceName  string
	portNumber  string
	counterName string
}

func (efa *efaSystem) HardwareCounters(ctx context.Context) ([]monitor.Condition, error) {
	pathPattern := config.ToHostPath(filepath.Join(infinibandSysfsPath, "/*/ports/*/hw_counters/"))
	paths, err := filepath.Glob(pathPattern)
	if err != nil {
		return nil, err
	}

	// NOTE: the index is discovered dynamically based on the pattern, but is
	// dependent on where the host path is mounted (eg. the pod form-factor of
	// the agent mounts it as '/host', making the index an additonal +1).
	deviceNameIndex := globIndex(pathPattern, 0)
	if deviceNameIndex < 0 {
		return nil, fmt.Errorf("failed to find glob at index %d within pattern: %q", 0, pathPattern)
	}
	portNumberIndex := globIndex(pathPattern, 1)
	if portNumberIndex < 0 {
		return nil, fmt.Errorf("failed to find glob at index %d within pattern: %q", 1, pathPattern)
	}

	var conditions []monitor.Condition
	for _, counterPath := range paths {
		var (
			pathParts  = strings.Split(counterPath, string(os.PathSeparator))
			deviceName = pathParts[deviceNameIndex]
			portNumber = pathParts[portNumberIndex]
		)

		logger := log.FromContext(ctx).WithValues("device", deviceName, "port", portNumber)

		vendorId, deviceId, err := readInfinibandDeviceInfo(deviceName)
		if err != nil {
			logger.V(4).Info("skipping device after failure reading device info", "error", err)
			continue
		}
		if vendorId != EFA_VENDOR_ID || !strings.HasPrefix(deviceId, EFA_DEVICE_ID_PREFIX) {
			logger.V(6).Info("skipping non-efa device", "vendorId", vendorId, "deviceId", deviceId)
			continue
		}

		filepath.WalkDir(counterPath, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return fs.SkipDir
			}
			// skip the counter directory itself
			if path == counterPath {
				return nil
			}
			// dont recurse into subfolders
			if entry.IsDir() {
				return fs.SkipDir
			}

			// the counter metric name maps to the filename
			counterName := entry.Name()
			// the key must be unique for each metric across each of the
			// hardware devices.
			counterKey := counterKey{
				deviceName:  deviceName,
				portNumber:  portNumber,
				counterName: counterName,
			}

			tracker, ok := efa.counterTrackers[counterKey]
			if !ok {
				trackerConstructor, exists := efa.counterConstructors[counterName]
				// if we can't key the counter name into the constructor map,
				// then this metric is not one that we measure.
				if !exists {
					logger.V(6).Info("skipping hardware counter without handler", "path", path, "counterName", counterName)
					return nil
				}
				// initialize the counter handler using the device information
				tracker = trackerConstructor(deviceName, portNumber)
				efa.counterTrackers[counterKey] = tracker
			}
			counterValue, err := osext.ReadInt(path)
			if err != nil {
				logger.V(4).Info("failed to read hardware counter", "error", err, "path", path)
				return nil
			}

			conditions = append(conditions, tracker.Process(counterName, counterValue)...)
			return nil
		})
	}
	return conditions, nil
}

// CountActiveEFADevices returns the number of EFA devices under
// /sys/class/infiniband with a port that is ACTIVE and LinkUp. Being registered
// there only means the driver set the device up, not that its link works.
func CountActiveEFADevices() (int, error) {
	entries, err := os.ReadDir(config.ToHostPath(infinibandSysfsPath))
	if err != nil {
		// nodes without any rdma devices do not have the infiniband class.
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		// NOTE: entries are symlinks into the pci device tree, so we don't
		// filter on entry.IsDir().
		vendorId, deviceId, err := readInfinibandDeviceInfo(entry.Name())
		if err != nil {
			continue
		}
		if vendorId == EFA_VENDOR_ID && strings.HasPrefix(deviceId, EFA_DEVICE_ID_PREFIX) && hasActivePort(entry.Name()) {
			count++
		}
	}
	return count, nil
}

// hasActivePort reports whether any port of the infiniband device has a
// logical state of ACTIVE and a physical state of LinkUp.
func hasActivePort(deviceName string) bool {
	portDirs, err := filepath.Glob(config.ToHostPath(filepath.Join(infinibandSysfsPath, deviceName, "ports", "*")))
	if err != nil {
		return false
	}
	for _, portDir := range portDirs {
		if readPortState(filepath.Join(portDir, "state")) == "ACTIVE" && readPortState(filepath.Join(portDir, "phys_state")) == "LinkUp" {
			return true
		}
	}
	return false
}

// readPortState returns the name from a port state file, which contains a
// value like "4: ACTIVE" or "5: LinkUp", or an empty string if it can't be read.
func readPortState(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_, name, _ := strings.Cut(string(data), ":")
	return strings.TrimSpace(name)
}

// EFAPCIDevices summarizes the EFA PCI devices on the node.
type EFAPCIDevices struct {
	// Attached is the number of EFA PCI devices. The instance exposes one for
	// each EFA interface attached at launch, whether or not the efa driver
	// loads, so zero means EFA was not requested.
	Attached int
	// Bound is the number of those devices bound to the efa driver.
	Bound int
}

// CountEFAPCIDevices returns the EFA PCI devices on the node and how many of
// them are bound to the efa driver.
func CountEFAPCIDevices() (EFAPCIDevices, error) {
	var devices EFAPCIDevices
	entries, err := os.ReadDir(config.ToHostPath(pciDevicesSysfsPath))
	if err != nil {
		if os.IsNotExist(err) {
			return devices, nil
		}
		return devices, err
	}
	for _, entry := range entries {
		// NOTE: entries are symlinks into the pci device tree, so we don't
		// filter on entry.IsDir().
		deviceDir := config.ToHostPath(filepath.Join(pciDevicesSysfsPath, entry.Name()))
		vendorId, deviceId, err := readDeviceInfo(deviceDir)
		if err != nil {
			continue
		}
		if vendorId != EFA_VENDOR_ID || !strings.HasPrefix(deviceId, EFA_DEVICE_ID_PREFIX) {
			continue
		}
		devices.Attached++
		// the driver link only exists while a driver is bound to the device.
		if driver, err := os.Readlink(filepath.Join(deviceDir, "driver")); err == nil && filepath.Base(driver) == efaDriverName {
			devices.Bound++
		}
	}
	return devices, nil
}

// PCIeLink is the speed and width of a PCIe link.
type PCIeLink struct {
	// SpeedGTs is the per-lane speed in GT/s, for example 32 for PCIe Gen5.
	SpeedGTs float64
	// Width is the number of lanes, for example 16.
	Width int
}

func (l PCIeLink) String() string {
	return fmt.Sprintf("x%d at %s GT/s", l.Width, strconv.FormatFloat(l.SpeedGTs, 'f', -1, 64))
}

// EFAPCIeLink is the PCIe link state of one EFA PCI device.
type EFAPCIeLink struct {
	// Address is the PCI address of the device, for example 0000:31:00.0.
	Address string
	// Current is the link the device trained to.
	Current PCIeLink
	// Capable is the best link the device can train to: the lower maximum
	// speed and width of the device and of the port it is connected to, since
	// a link can only be as fast as the slower of its two ends.
	Capable PCIeLink
}

// Degraded reports whether the link trained below what it is capable of.
func (l EFAPCIeLink) Degraded() bool {
	return l.Current.SpeedGTs < l.Capable.SpeedGTs || l.Current.Width < l.Capable.Width
}

// EFAPCIeLinks returns the PCIe link state of each EFA PCI device. Devices
// whose link state is not reported, or is reported as unknown, are skipped.
func EFAPCIeLinks() ([]EFAPCIeLink, error) {
	entries, err := os.ReadDir(config.ToHostPath(pciDevicesSysfsPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var links []EFAPCIeLink
	for _, entry := range entries {
		deviceDir := config.ToHostPath(filepath.Join(pciDevicesSysfsPath, entry.Name()))
		vendorId, deviceId, err := readDeviceInfo(deviceDir)
		if err != nil || vendorId != EFA_VENDOR_ID || !strings.HasPrefix(deviceId, EFA_DEVICE_ID_PREFIX) {
			continue
		}
		if link, ok := readEFAPCIeLink(deviceDir); ok {
			link.Address = entry.Name()
			links = append(links, link)
		}
	}
	return links, nil
}

// readEFAPCIeLink reads the current and capable link of the device in
// deviceDir. It returns false when the device does not report its link.
func readEFAPCIeLink(deviceDir string) (EFAPCIeLink, bool) {
	current, ok := readPCIeLink(deviceDir, "current_link_speed", "current_link_width")
	if !ok {
		return EFAPCIeLink{}, false
	}
	capable, ok := readPCIeLink(deviceDir, "max_link_speed", "max_link_width")
	if !ok {
		return EFAPCIeLink{}, false
	}
	// A link is trained between the device and the port it is connected to,
	// so it can only be as fast as the slower of the two. /sys/bus/pci/devices
	// entries link into the device tree, where each device sits in the
	// directory of that port. Ports further up are on separate links, which
	// limit bandwidth but not how this link trains, so they are not used.
	if devicePath, err := filepath.EvalSymlinks(deviceDir); err == nil {
		if port, ok := readPCIeLink(filepath.Dir(devicePath), "max_link_speed", "max_link_width"); ok {
			capable.SpeedGTs = min(capable.SpeedGTs, port.SpeedGTs)
			capable.Width = min(capable.Width, port.Width)
		}
	}
	return EFAPCIeLink{Current: current, Capable: capable}, true
}

// readPCIeLink reads a link speed and width from sysfs. Speeds look like
// "32.0 GT/s PCIe", or "8 GT/s" on older kernels. It returns false when either
// file is missing or reports an unknown value, such as "Unknown" or a width
// of 0.
// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-bus-pci
func readPCIeLink(dir, speedFile, widthFile string) (PCIeLink, bool) {
	speedData, err := os.ReadFile(filepath.Join(dir, speedFile))
	if err != nil {
		return PCIeLink{}, false
	}
	widthData, err := os.ReadFile(filepath.Join(dir, widthFile))
	if err != nil {
		return PCIeLink{}, false
	}
	speedFields := strings.Fields(string(speedData))
	if len(speedFields) == 0 {
		return PCIeLink{}, false
	}
	speed, err := strconv.ParseFloat(speedFields[0], 64)
	if err != nil || speed <= 0 {
		return PCIeLink{}, false
	}
	width, err := strconv.Atoi(strings.TrimSpace(string(widthData)))
	if err != nil || width <= 0 {
		return PCIeLink{}, false
	}
	return PCIeLink{SpeedGTs: speed, Width: width}, true
}

type deviceCounterTracker struct {
	counterValue int
	rateLimiter  *rate.Limiter
	name         string
	port         string
}

func (dct *deviceCounterTracker) Process(counterName string, counterValue int) []monitor.Condition {
	// update the current counter value after the check is complete
	defer func() { dct.counterValue = counterValue }()

	// only propagated errors onces they burst over an expected threshold.
	// we might expect that slow creep in errors is not fatal, so this
	// mechanism is used while we dont have an authoritative approach.
	if delta := counterValue - dct.counterValue; !dct.rateLimiter.AllowN(time.Now(), delta) {
		return []monitor.Condition{
			reasons.EFAErrorMetric.
				Builder().
				Message(fmt.Sprintf("%s increased from %d to %d for device:port [%s:%s]", counterName, dct.counterValue, counterValue, dct.name, dct.port)).
				Build(),
		}
	}
	return nil
}

// globIndex splits a path by the path separator and returns the index into the
// slices corresponding to the i'th glob (i.e. '*') in the pattern. if the i'th
// glob cannot be found, globIndex will return -1.
func globIndex(pathPattern string, index int) int {
	count := 0
	for i, part := range strings.Split(pathPattern, string(os.PathSeparator)) {
		if part == "*" {
			if count == index {
				return i
			}
			count++
		}
	}
	return -1
}

func readInfinibandDeviceInfo(deviceName string) (string, string, error) {
	return readDeviceInfo(config.ToHostPath(filepath.Join(infinibandSysfsPath, deviceName, "/device")))
}

// readDeviceInfo reads the vendor and device ids of the pci device at deviceDir.
func readDeviceInfo(deviceDir string) (string, string, error) {
	vendor, err := os.ReadFile(filepath.Join(deviceDir, "vendor"))
	if err != nil {
		return "", "", err
	}
	device, err := os.ReadFile(filepath.Join(deviceDir, "device"))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(vendor)), strings.TrimSpace(string(device)), nil
}
