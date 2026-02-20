package efa

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	deviceDir := config.ToHostPath(filepath.Join(infinibandSysfsPath, deviceName, "/device"))
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
