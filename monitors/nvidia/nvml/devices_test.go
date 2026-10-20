package nvml

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
)

// healthyFabricMask is a mask a healthy NVSwitch GPU reports: every boolean
// sub-field is False, which is 2, so it is non-zero.
const healthyFabricMask = 0x80

type fakeInstanceInfo struct {
	info *instanceinfo.InstanceInfo
	err  error
}

func (f fakeInstanceInfo) GetInstanceInfo(context.Context) (*instanceinfo.InstanceInfo, error) {
	return f.info, f.err
}

// devicesHarness wires Devices to fake GPUs, fake device files and a fake
// clock.
type devicesHarness struct {
	d        *Devices
	lib      *mock.Interface
	gpus     []*fakeGPU
	files    uint
	filesErr error
	now      time.Time
}

type devicesOptions struct {
	gpus           int
	info           instanceinfo.InstanceTypeInfoProvider
	noFabricSymbol bool // the loaded driver lacks nvmlDeviceGetGpuFabricInfoV
}

func newDevicesHarness(t *testing.T, o devicesOptions) *devicesHarness {
	t.Helper()
	h := &devicesHarness{files: uint(o.gpus), now: time.Unix(0, 0)}
	for range o.gpus {
		g := newFakeGPU()
		g.fabricState, g.fabricStatus, g.fabricMask = gonvml.GPU_FABRIC_STATE_COMPLETED, uint32(gonvml.SUCCESS), healthyFabricMask
		h.gpus = append(h.gpus, g)
	}
	h.lib = fakeLibrary(h.gpus...)
	if o.noFabricSymbol {
		h.lib.ExtensionsFunc = func() gonvml.ExtendedInterface {
			return &mock.ExtendedInterface{LookupSymbolFunc: func(name string) error {
				if name == "nvmlDeviceGetGpuFabricInfoV" {
					return errors.New("undefined symbol")
				}
				return nil
			}}
		}
	}
	byDevice := map[gonvml.Device]*fakeGPU{}
	for i, g := range h.gpus {
		dev, _ := h.lib.DeviceGetHandleByIndex(i)
		byDevice[dev] = g
	}
	fabric := func(dev gonvml.Device, call string) (*fakeGPU, gonvml.Return) {
		g := byDevice[dev]
		g.fabricReads++
		if g.block != nil {
			<-g.block
		}
		return g, g.result(call)
	}

	initializedReader(t, h.lib, func(r *Reader) { h.d = NewDevices(r, o.info) })
	h.d.now = func() time.Time { return h.now }
	h.d.deviceFiles = func() (uint, string, error) { return h.files, "/host/dev", h.filesErr }
	h.d.fabricV3 = func(dev gonvml.Device) (gonvml.GpuFabricInfo_v3, gonvml.Return) {
		g, ret := fabric(dev, "fabricV3")
		return gonvml.GpuFabricInfo_v3{State: g.fabricState, Status: g.fabricStatus, HealthMask: g.fabricMask}, ret
	}
	h.d.fabricV2 = func(dev gonvml.Device) (gonvml.GpuFabricInfo_v2, gonvml.Return) {
		g, ret := fabric(dev, "fabricV2")
		return gonvml.GpuFabricInfo_v2{State: g.fabricState, Status: g.fabricStatus, HealthMask: g.fabricMask}, ret
	}
	return h
}

// poll runs one poll and requires that it read the GPUs.
func (h *devicesHarness) poll(t *testing.T) []string {
	t.Helper()
	conds, ok := h.d.Poll(t.Context())
	require.True(t, ok, "the poll should have read the GPUs")
	return describe(conds)
}

func TestDevices(t *testing.T) {
	lost := gonvml.ERROR_GPU_IS_LOST
	for _, tc := range []struct {
		name  string
		gpus  int
		setup func(h *devicesHarness)
		want  []string // reason|severity|message
	}{
		{
			name:  "healthy GPUs report nothing",
			gpus:  2,
			setup: func(*devicesHarness) {},
		},
		{
			name: "fabric not supported, not started or still training reports nothing",
			gpus: 3,
			setup: func(h *devicesHarness) {
				h.gpus[0].fabricState = gonvml.GPU_FABRIC_STATE_NOT_SUPPORTED
				h.gpus[1].fabricState = gonvml.GPU_FABRIC_STATE_NOT_STARTED
				h.gpus[2].fabricState = gonvml.GPU_FABRIC_STATE_IN_PROGRESS
			},
		},
		{
			name: "fabric training that completed with an error is Fatal",
			gpus: 1,
			setup: func(h *devicesHarness) {
				h.gpus[0].fabricStatus = uint32(gonvml.ERROR_UNKNOWN)
			},
			want: []string{"FabricManagerNotRunning|Fatal|Fabric Manager status: Failure"},
		},
		{
			name: "a fabric state NVML added later is a Warning",
			gpus: 1,
			setup: func(h *devicesHarness) {
				h.gpus[0].fabricState = 9
			},
			want: []string{"FabricManagerNotRunning|Warning|Fabric Manager status: Unrecognized"},
		},
		{
			name: "a faulting fabric health mask is Fatal",
			gpus: 1,
			setup: func(h *devicesHarness) {
				h.gpus[0].fabricMask = healthyFabricMask | 0x10 // route_unhealthy=True
			},
			want: []string{"NvidiaFabricError|Fatal|GPU fabric health mask 0x90: route_unhealthy=1"},
		},
		{
			name: "a driver that rejects the v3 struct is read with v2",
			gpus: 1,
			setup: func(h *devicesHarness) {
				h.gpus[0].fail["fabricV3"] = gonvml.ERROR_ARGUMENT_VERSION_MISMATCH
				h.gpus[0].fabricStatus = uint32(gonvml.ERROR_UNKNOWN)
			},
			want: []string{"FabricManagerNotRunning|Fatal|Fabric Manager status: Failure"},
		},
		{
			name: "a fabric read that fails is not observed",
			gpus: 1,
			setup: func(h *devicesHarness) {
				h.gpus[0].fail["fabricV3"] = gonvml.ERROR_UNKNOWN
				h.gpus[0].fabricStatus = uint32(gonvml.ERROR_UNKNOWN)
			},
		},
		{
			name: "a GPU count that differs from the device files is Fatal",
			gpus: 2,
			setup: func(h *devicesHarness) {
				h.files = 3
			},
			want: []string{"NvidiaDeviceCountMismatch|Fatal|DCGM detected 2 GPUs but 3 nvidia device files were detected at /host/dev"},
		},
		{
			name: "device files that can't be counted skip that comparison",
			gpus: 2,
			setup: func(h *devicesHarness) {
				h.files, h.filesErr = 3, errors.New("bad pattern")
			},
		},
		{
			name: "a GPU lost at its handle reports only that it is lost",
			gpus: 2,
			setup: func(h *devicesHarness) {
				h.files = 3 // would be a count mismatch if the counts were compared
				h.gpus[1].fail["handle"] = lost
			},
			want: []string{"NvidiaXID79Error|Fatal|GPU 1 has fallen off the bus: NVML returned GPU_IS_LOST"},
		},
		{
			name: "a GPU lost at its fabric read reports only that it is lost",
			gpus: 2,
			setup: func(h *devicesHarness) {
				h.gpus[0].fabricStatus = uint32(gonvml.ERROR_UNKNOWN) // would be Fatal if it were classified
				h.gpus[0].fail["fabricV3"] = lost
			},
			want: []string{"NvidiaXID79Error|Fatal|GPU 0 has fallen off the bus: NVML returned GPU_IS_LOST"},
		},
		{
			name: "each lost GPU reports once",
			gpus: 3,
			setup: func(h *devicesHarness) {
				h.gpus[0].fail["handle"] = lost
				h.gpus[2].fail["fabricV3"] = lost
			},
			want: []string{
				"NvidiaXID79Error|Fatal|GPU 0 has fallen off the bus: NVML returned GPU_IS_LOST",
				"NvidiaXID79Error|Fatal|GPU 2 has fallen off the bus: NVML returned GPU_IS_LOST",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDevicesHarness(t, devicesOptions{gpus: tc.gpus})
			tc.setup(h)
			assert.Equal(t, tc.want, h.poll(t))
		})
	}
}

func TestDevices_missingFabricSymbolMakesNoCall(t *testing.T) {
	h := newDevicesHarness(t, devicesOptions{gpus: 2, noFabricSymbol: true})
	assert.Equal(t, []string{"FabricManagerNotRunning|Warning|Fabric Manager status: NvmlTooOld"}, h.poll(t),
		"the same Warning on every GPU reports once")
	for i, g := range h.gpus {
		assert.Zero(t, g.fabricReads, "GPU %d: calling a symbol the driver lacks would abort the process", i)
	}
}

func TestDevices_fabricWarningsAreRateLimited(t *testing.T) {
	h := newDevicesHarness(t, devicesOptions{gpus: 1})
	h.gpus[0].fabricState = 9 // a fabric state NVML added later -> Unrecognized Warning
	warn := []string{"FabricManagerNotRunning|Warning|Fabric Manager status: Unrecognized"}

	assert.Equal(t, warn, h.poll(t))
	h.now = h.now.Add(DevicesInterval)
	assert.Empty(t, h.poll(t), "a repeat inside the re-notify interval is dropped")
	h.now = h.now.Add(30 * time.Minute)
	assert.Equal(t, warn, h.poll(t), "the Warning repeats once the interval has passed")

	h.gpus[0].fabricState = gonvml.GPU_FABRIC_STATE_COMPLETED // healthy again
	assert.Empty(t, h.poll(t))
	h.gpus[0].fabricState = 9
	assert.Equal(t, warn, h.poll(t), "after a clean poll the next Warning reports at once")
}

func TestDevices_instanceTypeCount(t *testing.T) {
	g6e := &instanceinfo.InstanceInfo{InstanceType: "g6e.12xlarge", NvidiaGPUCount: 4}
	for _, tc := range []struct {
		name string
		info instanceinfo.InstanceTypeInfoProvider
		want []string
	}{
		{
			name: "fewer GPUs than the instance type provides is Fatal",
			info: fakeInstanceInfo{info: g6e},
			want: []string{"NvidiaDeviceCountMismatch|Fatal|expected 4 GPUs for this instance type but only 3 were detected — possible hardware failure"},
		},
		{
			name: "an instance type missing from the lookup skips the comparison",
			info: fakeInstanceInfo{err: fmt.Errorf("%w: p99.large", instanceinfo.ErrUnknownInstanceType)},
		},
		{
			name: "an instance type that can't be read skips the comparison",
			info: fakeInstanceInfo{err: errors.New("IMDS unavailable")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A GPU that failed to enumerate at boot: NVML and /dev agree on 3.
			h := newDevicesHarness(t, devicesOptions{gpus: 3, info: tc.info})
			assert.Equal(t, tc.want, h.poll(t))
		})
	}
}

func TestDevices_unknown(t *testing.T) {
	t.Run("NVML not initialized", func(t *testing.T) {
		r := newReader(Config{}, time.Now, func() []string { return nil }, nil)
		conds, ok := NewDevices(r, nil).Poll(t.Context())
		assert.Empty(t, conds)
		assert.False(t, ok)
	})

	t.Run("GPU count unreadable", func(t *testing.T) {
		h := newDevicesHarness(t, devicesOptions{gpus: 1})
		h.lib.DeviceGetCountFunc = func() (int, gonvml.Return) { return 0, gonvml.ERROR_UNKNOWN }
		conds, ok := h.d.Poll(t.Context())
		assert.Empty(t, conds)
		assert.False(t, ok)
	})

	t.Run("stuck in the driver", func(t *testing.T) {
		h := newDevicesHarness(t, devicesOptions{gpus: 1})
		h.d.check.timeout = 20 * time.Millisecond
		h.files = 2 // would be a count mismatch if the poll were read
		release := make(chan struct{})
		h.gpus[0].block = release
		for _, msg := range []string{"the stuck poll", "the next poll, which skips while the call is in the driver"} {
			conds, ok := h.d.Poll(t.Context())
			assert.Empty(t, conds, msg)
			assert.False(t, ok, "%s is unknown, not clean", msg)
		}
		assert.Equal(t, 2, h.d.check.StuckCycles())

		close(release)
		select {
		case <-h.d.check.pending:
		case <-time.After(time.Second):
			t.Fatal("the released call never returned")
		}
		assert.Equal(t, 1, h.gpus[0].fabricReads, "a call still in the driver is not repeated")
	})
}
