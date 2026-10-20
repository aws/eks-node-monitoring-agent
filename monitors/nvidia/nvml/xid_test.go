package nvml

import (
	"context"
	"strconv"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

type waitResult struct {
	data gonvml.EventData
	ret  gonvml.Return
}

// xidHarness wires XIDs to a fake library whose event set returns queued
// results, a fake clock, and a recorded sleep.
type xidHarness struct {
	x       *XIDs
	lib     *mock.Interface
	devices []gonvml.Device
	queue   []waitResult
	now     time.Time
	slept   []time.Duration
	creates int
}

func newXIDHarness(t *testing.T, gpus ...*fakeGPU) *xidHarness {
	h := &xidHarness{lib: fakeLibrary(gpus...), now: time.Unix(0, 0)}
	for i := range gpus {
		dev, _ := h.lib.DeviceGetHandleByIndex(i)
		h.devices = append(h.devices, dev)
	}
	set := &mock.EventSet{
		WaitFunc: func(timeoutMs uint32) (gonvml.EventData, gonvml.Return) {
			if timeoutMs != 5000 {
				t.Errorf("Wait timeout %d ms, want 5000", timeoutMs)
			}
			if len(h.queue) == 0 {
				return gonvml.EventData{}, gonvml.ERROR_TIMEOUT
			}
			next := h.queue[0]
			h.queue = h.queue[1:]
			return next.data, next.ret
		},
		FreeFunc: func() gonvml.Return { return gonvml.SUCCESS },
	}
	h.lib.EventSetCreateFunc = func() (gonvml.EventSet, gonvml.Return) {
		h.creates++
		return set, gonvml.SUCCESS
	}
	initializedReader(t, h.lib, func(r *Reader) { h.x = NewXIDs(r) })
	h.x.now = func() time.Time { return h.now }
	h.x.sleep = func(_ context.Context, d time.Duration) { h.slept = append(h.slept, d) }
	return h
}

// wait calls Wait and requires that it waited in the driver.
func (h *xidHarness) wait(t *testing.T) []monitor.Condition {
	t.Helper()
	conds, ok := h.x.Wait(t.Context())
	require.True(t, ok, "Wait should have waited in the driver")
	return conds
}

// unknown calls Wait and requires that it could not wait in the driver.
func unknown(t *testing.T, x *XIDs) {
	t.Helper()
	conds, ok := x.Wait(t.Context())
	assert.Empty(t, conds)
	assert.False(t, ok, "Wait should report that it could not wait")
}

func (h *xidHarness) event(gpu int, xid uint64) {
	h.queue = append(h.queue, waitResult{gonvml.EventData{
		Device:    h.devices[gpu],
		EventType: gonvml.EventTypeXidCriticalError,
		EventData: xid,
	}, gonvml.SUCCESS})
}

func TestXIDs_events(t *testing.T) {
	h := newXIDHarness(t, newFakeGPU(), newFakeGPU())

	h.event(0, 79)
	assert.Equal(t, []string{"NvidiaXID79Error|Fatal|detected XID-79 on the instance, review kernel logs for additional information."},
		describe(h.wait(t)))
	h.event(1, 13)
	assert.Equal(t, []string{"NvidiaXID13Warning|Warning|detected unknown XID-13 on the instance, review kernel logs for additional information."},
		describe(h.wait(t)))

	assert.Empty(t, h.wait(t), "a wait that times out returns nothing, and counts as a quiet period")
	assert.Empty(t, h.slept, "a timed-out wait does not pause again")

	h.queue = append(h.queue, waitResult{gonvml.EventData{Device: h.devices[0], EventType: 1 << 40}, gonvml.SUCCESS})
	assert.Empty(t, h.wait(t), "events other than XIDs are ignored")

	assert.Equal(t, 1, h.creates, "the event set is created once")
}

// TestXIDs_repeatFloor checks that a GPU reports at most one XID every five
// seconds, as DCGM's policy does, while other GPUs are unaffected.
func TestXIDs_repeatFloor(t *testing.T) {
	h := newXIDHarness(t, newFakeGPU(), newFakeGPU())

	h.event(0, 13)
	require.Len(t, h.wait(t), 1)

	h.now = h.now.Add(4 * time.Second)
	h.event(0, 79)
	assert.Empty(t, h.wait(t), "a second XID on the same GPU inside five seconds is dropped")
	h.event(1, 79)
	assert.Len(t, h.wait(t), 1, "another GPU is not held back")

	h.now = h.now.Add(time.Second)
	h.event(0, 79)
	assert.Len(t, h.wait(t), 1, "five seconds later the GPU reports again")
}

func TestXIDs_unsupportedGPUIsNotRetried(t *testing.T) {
	unsupported := newFakeGPU()
	unsupported.fail["registerEvents"] = gonvml.ERROR_NOT_SUPPORTED
	h := newXIDHarness(t, newFakeGPU(), unsupported)

	assert.Empty(t, h.wait(t))
	assert.Empty(t, h.x.missing)
	assert.Equal(t, float64(0), testutil.ToFloat64(xidUnregisteredGPUs))
	h.now = h.now.Add(xidRetryInterval)
	assert.Empty(t, h.wait(t))
	assert.Equal(t, 1, unsupported.registers, "a GPU without XID events is not retried")
}

// TestXIDs_retriesFailedRegistration checks that a GPU that fails to register
// is retried into the same event set at most every xidRetryInterval, and is
// counted in nvml_xid_unregistered_gpus until it registers.
func TestXIDs_retriesFailedRegistration(t *testing.T) {
	t.Run("registration fails", func(t *testing.T) {
		flaky := newFakeGPU()
		flaky.fail["registerEvents"] = gonvml.ERROR_UNKNOWN
		h := newXIDHarness(t, newFakeGPU(), flaky)

		assert.Empty(t, h.wait(t))
		assert.Equal(t, []int{1}, h.x.missing)
		assert.Equal(t, float64(1), testutil.ToFloat64(xidUnregisteredGPUs))

		h.now = h.now.Add(xidRetryInterval - time.Second)
		assert.Empty(t, h.wait(t))
		assert.Equal(t, 1, flaky.registers, "no retry before the interval")

		h.now = h.now.Add(time.Second)
		assert.Empty(t, h.wait(t))
		assert.Equal(t, 2, flaky.registers, "retried after the interval")
		assert.Equal(t, []int{1}, h.x.missing)

		delete(flaky.fail, "registerEvents")
		h.now = h.now.Add(xidRetryInterval)
		assert.Empty(t, h.wait(t))
		assert.Empty(t, h.x.missing)
		assert.Equal(t, float64(0), testutil.ToFloat64(xidUnregisteredGPUs))
		assert.Equal(t, 1, h.creates, "retries reuse the event set")

		h.event(1, 79)
		assert.Len(t, h.wait(t), 1, "the retried GPU's XIDs are received")
	})

	t.Run("device handle fails", func(t *testing.T) {
		gpu := newFakeGPU()
		h := newXIDHarness(t, newFakeGPU(), gpu)
		device := h.lib.DeviceGetHandleByIndexFunc
		h.lib.DeviceGetHandleByIndexFunc = func(i int) (gonvml.Device, gonvml.Return) {
			if i == 1 {
				return nil, gonvml.ERROR_UNKNOWN
			}
			return device(i)
		}

		assert.Empty(t, h.wait(t))
		assert.Equal(t, []int{1}, h.x.missing)
		assert.Zero(t, gpu.registers)

		h.lib.DeviceGetHandleByIndexFunc = device
		h.now = h.now.Add(xidRetryInterval)
		assert.Empty(t, h.wait(t))
		assert.Empty(t, h.x.missing)
		assert.Equal(t, 1, gpu.registers)
	})
}

// TestXIDs_unknownDevicesHaveTheirOwnFloor checks that the repeat floor is
// per device, including devices NMA did not register.
func TestXIDs_unknownDevicesHaveTheirOwnFloor(t *testing.T) {
	h := newXIDHarness(t, newFakeGPU())
	for _, dev := range []gonvml.Device{&mock.Device{}, &mock.Device{}} {
		h.queue = append(h.queue, waitResult{gonvml.EventData{Device: dev, EventType: gonvml.EventTypeXidCriticalError, EventData: 79}, gonvml.SUCCESS})
		assert.Len(t, h.wait(t), 1)
	}
}

// TestXIDs_noBusyLoop checks that Wait pauses instead of returning at once
// whenever it cannot wait in the driver, so a caller looping on it does not
// spin.
func TestXIDs_noBusyLoop(t *testing.T) {
	t.Run("not initialized", func(t *testing.T) {
		r := newReader(Config{}, time.Now, func() []string { return nil }, nil)
		x := NewXIDs(r)
		var slept []time.Duration
		x.sleep = func(_ context.Context, d time.Duration) { slept = append(slept, d) }
		unknown(t, x)
		assert.Equal(t, []time.Duration{xidWaitTimeout}, slept)
	})

	t.Run("event set cannot be created", func(t *testing.T) {
		h := newXIDHarness(t, newFakeGPU())
		h.lib.EventSetCreateFunc = func() (gonvml.EventSet, gonvml.Return) { return nil, gonvml.ERROR_UNKNOWN }
		h.x.set = nil
		unknown(t, h.x)
		assert.Equal(t, []time.Duration{xidWaitTimeout}, h.slept)
	})

	t.Run("wait error", func(t *testing.T) {
		h := newXIDHarness(t, newFakeGPU())
		counter := unexpectedReturns.WithLabelValues("nvmlEventSetWait", strconv.Itoa(int(gonvml.ERROR_UNKNOWN)))
		before := testutil.ToFloat64(counter)
		h.queue = append(h.queue, waitResult{ret: gonvml.ERROR_UNKNOWN})
		unknown(t, h.x)
		assert.Equal(t, []time.Duration{xidWaitTimeout}, h.slept)
		assert.Equal(t, before+1, testutil.ToFloat64(counter))
	})

	t.Run("stuck wait", func(t *testing.T) {
		h := newXIDHarness(t, newFakeGPU())
		h.x.check.timeout = 20 * time.Millisecond
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		h.x.set = &mock.EventSet{WaitFunc: func(uint32) (gonvml.EventData, gonvml.Return) {
			<-release
			return gonvml.EventData{}, gonvml.ERROR_TIMEOUT
		}}
		unknown(t, h.x)
		unknown(t, h.x)
		assert.Equal(t, []time.Duration{xidWaitTimeout, xidWaitTimeout}, h.slept)
		assert.Equal(t, 2, h.x.check.StuckCycles())
	})
}

func TestSleepContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	sleepContext(ctx, time.Hour)
	assert.Less(t, time.Since(start), time.Second, "a cancelled context ends the sleep")
}
