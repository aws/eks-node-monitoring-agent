package nvml

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

// fakeGPU is one GPU's NVML values. Tests change them between polls. Polls
// run one at a time, and run's goroutine start and result channel order a
// test's writes before the next poll's reads, so the fake needs no lock.
type fakeGPU struct {
	dbe      uint64
	replays  int
	sbePages int
	dbePages int
	tempC    uint32
	powerMW  uint32
	limitMW  uint32
	nvlink   [4]uint64 // in nvlinkErrorTotals order
	fail     map[string]gonvml.Return
	block    chan struct{} // when set, the ECC read waits on it

	limitReads int
	afterLost  int // calls made after a call returned ERROR_GPU_IS_LOST
	lost       bool
}

func newFakeGPU() *fakeGPU {
	return &fakeGPU{tempC: 40, powerMW: 300_000, limitMW: 700_000, fail: map[string]gonvml.Return{}}
}

// result returns call's configured return code.
func (f *fakeGPU) result(call string) gonvml.Return {
	if f.lost {
		f.afterLost++
		return gonvml.ERROR_GPU_IS_LOST
	}
	if ret, ok := f.fail[call]; ok {
		if ret == gonvml.ERROR_GPU_IS_LOST {
			f.lost = true
		}
		return ret
	}
	return gonvml.SUCCESS
}

func (f *fakeGPU) device() *mock.Device {
	return &mock.Device{
		GetMemoryErrorCounterFunc: func(gonvml.MemoryErrorType, gonvml.EccCounterType, gonvml.MemoryLocation) (uint64, gonvml.Return) {
			if f.block != nil {
				<-f.block
			}
			return f.dbe, f.result("ecc")
		},
		GetPcieReplayCounterFunc: func() (int, gonvml.Return) {
			return f.replays, f.result("pcie")
		},
		GetRetiredPagesFunc: func(cause gonvml.PageRetirementCause) ([]uint64, gonvml.Return) {
			if cause == gonvml.PAGE_RETIREMENT_CAUSE_DOUBLE_BIT_ECC_ERROR {
				return make([]uint64, f.dbePages), f.result("dbePages")
			}
			return make([]uint64, f.sbePages), f.result("sbePages")
		},
		GetTemperatureFunc: func(gonvml.TemperatureSensors) (uint32, gonvml.Return) {
			return f.tempC, f.result("temperature")
		},
		GetPowerUsageFunc: func() (uint32, gonvml.Return) {
			return f.powerMW, f.result("power")
		},
		GetEnforcedPowerLimitFunc: func() (uint32, gonvml.Return) {
			f.limitReads++
			return f.limitMW, f.result("powerLimit")
		},
		GetFieldValuesFunc: func(values []gonvml.FieldValue) gonvml.Return {
			ret := f.result("fieldValues")
			if ret != gonvml.SUCCESS {
				return ret
			}
			for i := range values {
				for j, total := range nvlinkErrorTotals {
					if values[i].FieldId == total.nvmlField {
						values[i].ValueType = uint32(gonvml.VALUE_TYPE_UNSIGNED_LONG_LONG)
						binary.NativeEndian.PutUint64(values[i].Value[:], f.nvlink[j])
						values[i].NvmlReturn = uint32(f.result(fmt.Sprintf("nvlink%d", j)))
					}
				}
			}
			return gonvml.SUCCESS
		},
		RegisterEventsFunc: func(uint64, gonvml.EventSet) gonvml.Return {
			return f.result("registerEvents")
		},
	}
}

// fakeLibrary returns an NVML library with the given GPUs. Each GPU has one
// device handle, as in NVML.
func fakeLibrary(gpus ...*fakeGPU) *mock.Interface {
	devices := make([]gonvml.Device, len(gpus))
	for i, g := range gpus {
		devices[i] = g.device()
	}
	return &mock.Interface{
		InitFunc: func() gonvml.Return { return gonvml.SUCCESS },
		ExtensionsFunc: func() gonvml.ExtendedInterface {
			return &mock.ExtendedInterface{LookupSymbolFunc: func(string) error { return nil }}
		},
		DeviceGetCountFunc:         func() (int, gonvml.Return) { return len(devices), gonvml.SUCCESS },
		DeviceGetHandleByIndexFunc: func(i int) (gonvml.Device, gonvml.Return) { return devices[i], gonvml.SUCCESS },
	}
}

// initializedReader returns a Reader that has initialized lib. register runs
// before the first Reconcile, where checks must be created.
func initializedReader(t *testing.T, lib gonvml.Interface, register func(*Reader)) *Reader {
	t.Helper()
	r := newReader(Config{}, time.Now, func() []string { return []string{"/host/usr/lib64/" + libraryName} },
		func(string) gonvml.Interface { return lib })
	register(r)
	require.Empty(t, r.Reconcile(t.Context()))
	_, _, ok := r.Library()
	require.True(t, ok)
	return r
}

// describe renders conditions as reason|severity|message for comparison.
func describe(conds []monitor.Condition) []string {
	var out []string
	for _, c := range conds {
		out = append(out, fmt.Sprintf("%s|%s|%s", c.Reason, c.Severity, c.Message))
	}
	return out
}

func TestPolicies(t *testing.T) {
	type poll struct {
		change func(gpus []*fakeGPU) // applied before the poll
		want   []string              // reason|severity|message
	}
	nothing := func([]*fakeGPU) {}

	for _, tc := range []struct {
		name           string
		gpus           int
		thresholdWatts uint32
		polls          []poll
	}{
		{
			name: "the first reading of a counter is its baseline",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].dbe, g[0].replays, g[0].nvlink = 3, 5, [4]uint64{7, 7, 7, 7} }, nil},
				{nothing, nil},
			},
		},
		{
			name: "double-bit ECC growth is Fatal",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].dbe = 3 }, nil},
				{func(g []*fakeGPU) { g[0].dbe = 5 }, []string{"NvidiaDoubleBitError|Fatal|detected 5 Nvidia Double Bit error(s) on location Device"}},
				{nothing, nil},
			},
		},
		{
			name: "NVLink reports the first counter that grew, and every counter keeps its baseline",
			gpus: 1,
			polls: []poll{
				{nothing, nil},
				{func(g []*fakeGPU) { g[0].nvlink = [4]uint64{0, 2, 0, 4} }, []string{"NvidiaNVLinkError|Fatal|detected 2 NVLink errors on fieldId 419"}},
				{func(g []*fakeGPU) { g[0].nvlink = [4]uint64{0, 2, 0, 5} }, []string{"NvidiaNVLinkError|Fatal|detected 5 NVLink errors on fieldId 439"}},
			},
		},
		{
			name: "PCIe replay growth is a Warning",
			gpus: 1,
			polls: []poll{
				{nothing, nil},
				{func(g []*fakeGPU) { g[0].replays = 9 }, []string{"NvidiaPCIeError|Warning|detected 9 PCIe replays"}},
			},
		},
		{
			name: "a counter that drops was reset and becomes the new baseline",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].dbe = 5 }, nil},
				{func(g []*fakeGPU) { g[0].dbe = 1 }, nil},
				{func(g []*fakeGPU) { g[0].dbe = 2 }, []string{"NvidiaDoubleBitError|Fatal|detected 2 Nvidia Double Bit error(s) on location Device"}},
			},
		},
		{
			name: "a failed read clears the baseline, so growth needs two good readings",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].dbe = 3 }, nil},
				{func(g []*fakeGPU) { g[0].fail["ecc"] = gonvml.ERROR_UNKNOWN }, nil},
				{func(g []*fakeGPU) { delete(g[0].fail, "ecc"); g[0].dbe = 9 }, nil},
				{func(g []*fakeGPU) { g[0].dbe = 10 }, []string{"NvidiaDoubleBitError|Fatal|detected 10 Nvidia Double Bit error(s) on location Device"}},
			},
		},
		{
			name: "retired pages above the limit, with both counts observed",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].sbePages, g[0].dbePages = 5, 5 }, nil},
				{func(g []*fakeGPU) { g[0].sbePages = 6 }, []string{"NvidiaPageRetirement|Warning|detected 6 SBE, and 5 DBE page retirements"}},
				{func(g []*fakeGPU) { g[0].fail["dbePages"] = gonvml.ERROR_NOT_SUPPORTED }, nil},
			},
		},
		{
			name: "temperature above 100 C reports on every poll",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].tempC = 100 }, nil},
				{func(g []*fakeGPU) { g[0].tempC = 101 }, []string{"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101"}},
				{nothing, []string{"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101"}},
			},
		},
		{
			name: "power compares whole watts against the enforced limit when no threshold is set",
			gpus: 1,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].powerMW = 700_999 }, nil},
				{func(g []*fakeGPU) { g[0].powerMW = 701_000 }, []string{"NvidiaPowerError|Warning|detected power usage outside of thresholds with severity code 701"}},
				{func(g []*fakeGPU) { g[0].fail["powerLimit"] = gonvml.ERROR_NOT_SUPPORTED }, nil},
			},
		},
		{
			name:           "a configured power threshold replaces the enforced limit",
			gpus:           1,
			thresholdWatts: 250,
			polls: []poll{
				{nothing, []string{"NvidiaPowerError|Warning|detected power usage outside of thresholds with severity code 300"}},
			},
		},
		{
			name: "each GPU reports on its own",
			gpus: 2,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].tempC, g[1].tempC = 101, 102 }, []string{
					"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101",
					"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 102",
				}},
			},
		},
		{
			name: "a lost GPU reports nothing here and its counters start over",
			gpus: 2,
			polls: []poll{
				{func(g []*fakeGPU) { g[0].dbe, g[1].tempC = 3, 101 }, []string{"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101"}},
				{func(g []*fakeGPU) { g[0].dbe, g[0].fail["ecc"] = 4, gonvml.ERROR_GPU_IS_LOST }, []string{"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101"}},
				{func(g []*fakeGPU) { g[0].lost = false; delete(g[0].fail, "ecc"); g[0].dbe = 5 }, []string{"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101"}},
				{func(g []*fakeGPU) { g[0].dbe = 6 }, []string{
					"NvidiaDoubleBitError|Fatal|detected 6 Nvidia Double Bit error(s) on location Device",
					"NvidiaThermalError|Warning|detected GPU thermals outside of thresholds with severity code 101",
				}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpus := make([]*fakeGPU, tc.gpus)
			for i := range gpus {
				gpus[i] = newFakeGPU()
			}
			var p *Policies
			initializedReader(t, fakeLibrary(gpus...), func(r *Reader) { p = NewPolicies(r) })
			p.SetPowerThresholdWatts(tc.thresholdWatts)

			// Polls run one at a time, and run's goroutine start and result
			// channel order these writes before the next poll's reads.
			for i, step := range tc.polls {
				step.change(gpus)
				conds, ok := p.Poll()
				assert.True(t, ok, "poll %d read the GPUs", i+1)
				assert.Equal(t, step.want, describe(conds), "poll %d", i+1)
			}
		})
	}
}

func TestPolicies_lostGPUStopsReading(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fail["ecc"] = gonvml.ERROR_GPU_IS_LOST
	var p *Policies
	initializedReader(t, fakeLibrary(gpu), func(r *Reader) { p = NewPolicies(r) })

	conds, ok := p.Poll()
	assert.Empty(t, conds)
	assert.True(t, ok, "the poll read the GPUs; the lost one is reported by its own check")
	assert.Zero(t, gpu.afterLost, "no call after the first ERROR_GPU_IS_LOST")
}

func TestPolicies_enforcedLimitReadOnlyWithoutThreshold(t *testing.T) {
	gpu := newFakeGPU()
	var p *Policies
	initializedReader(t, fakeLibrary(gpu), func(r *Reader) { p = NewPolicies(r) })

	p.Poll()
	assert.Equal(t, 1, gpu.limitReads)
	p.SetPowerThresholdWatts(250)
	p.Poll()
	assert.Equal(t, 1, gpu.limitReads, "a configured threshold skips the enforced-limit read")
}

func TestPolicies_notInitialized(t *testing.T) {
	r := newReader(Config{}, time.Now, func() []string { return nil }, nil)
	p := NewPolicies(r)
	conds, ok := p.Poll()
	assert.Empty(t, conds)
	assert.False(t, ok, "a poll before NVML initializes is unknown, not clean")
}

func TestPolicies_gpuCountUnreadable(t *testing.T) {
	lib := fakeLibrary(newFakeGPU())
	var p *Policies
	initializedReader(t, lib, func(r *Reader) { p = NewPolicies(r) })
	lib.DeviceGetCountFunc = func() (int, gonvml.Return) { return 0, gonvml.ERROR_UNKNOWN }
	conds, ok := p.Poll()
	assert.Empty(t, conds)
	assert.False(t, ok, "a poll that could not count the GPUs read nothing")
}

// TestPolicies_stuckPollKeepsBaselines checks that readings from a poll that
// outlived its deadline are dropped, so a counter that grew during the stall
// is reported by the next poll that answers.
func TestPolicies_stuckPollKeepsBaselines(t *testing.T) {
	gpu := newFakeGPU()
	gpu.dbe = 3
	var p *Policies
	initializedReader(t, fakeLibrary(gpu), func(r *Reader) { p = NewPolicies(r) })
	p.check.timeout = 20 * time.Millisecond

	conds, ok := p.Poll()
	require.Empty(t, conds)
	require.True(t, ok)

	release := make(chan struct{})
	gpu.dbe, gpu.block = 4, release
	for _, msg := range []string{"the stuck poll", "the next poll, which skips while the call is in the driver"} {
		conds, ok := p.Poll()
		assert.Empty(t, conds, msg)
		assert.False(t, ok, "%s is unknown, not clean", msg)
	}
	assert.Equal(t, 2, p.check.StuckCycles())

	close(release) // a closed channel no longer blocks later polls
	select {
	case <-p.check.pending:
	case <-time.After(time.Second):
		t.Fatal("the released call never returned")
	}
	conds, ok = p.Poll()
	assert.True(t, ok)
	assert.Equal(t, []string{"NvidiaDoubleBitError|Fatal|detected 4 Nvidia Double Bit error(s) on location Device"}, describe(conds))
	assert.Zero(t, p.check.StuckCycles())
}

func TestFieldValueUint(t *testing.T) {
	value := func(t gonvml.ValueType, n uint64) gonvml.FieldValue {
		v := gonvml.FieldValue{ValueType: uint32(t)}
		binary.NativeEndian.PutUint64(v.Value[:], n)
		return v
	}
	for _, tc := range []struct {
		in     gonvml.FieldValue
		want   uint64
		wantOK bool
	}{
		{value(gonvml.VALUE_TYPE_UNSIGNED_LONG_LONG, 1<<40), 1 << 40, true},
		{value(gonvml.VALUE_TYPE_UNSIGNED_LONG, 7), 7, true},
		{value(gonvml.VALUE_TYPE_UNSIGNED_INT, 9), 9, true},
		{value(gonvml.VALUE_TYPE_DOUBLE, 9), 0, false},
		{value(gonvml.VALUE_TYPE_SIGNED_LONG_LONG, 9), 0, false},
	} {
		got, ok := fieldValueUint(tc.in)
		assert.Equal(t, tc.wantOK, ok, "type %d", tc.in.ValueType)
		assert.Equal(t, tc.want, got, "type %d", tc.in.ValueType)
	}
}
