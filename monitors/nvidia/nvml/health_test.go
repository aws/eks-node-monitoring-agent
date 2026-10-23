package nvml

import (
	"errors"
	"math"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ber packs an NVLink BER the way NVML reports it.
func ber(mantissa, exponent uint64) uint64 {
	return mantissa<<gonvml.NVLINK_BER_MANTISSA_SHIFT | exponent<<gonvml.NVLINK_BER_EXP_SHIFT
}

// healthyFields are the health field values of a GPU with nothing wrong. It
// reports no NVLinks.
func healthyFields() map[uint32]uint64 {
	fields := map[uint32]uint64{}
	for _, f := range healthFields {
		fields[f.id] = 0
	}
	fields[gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER] = ber(berZeroMantissa, berZeroExponent)
	fields[gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER] = ber(berZeroMantissa, berZeroExponent)
	return fields
}

// newHealthHarness returns an initialized Health over gpus, on a fake clock.
func newHealthHarness(t *testing.T, gpus ...*fakeGPU) (*Health, *time.Time) {
	t.Helper()
	var h *Health
	initializedReader(t, fakeLibrary(gpus...), func(r *Reader) { h = NewHealth(r) })
	now := time.Unix(0, 0)
	h.now = func() time.Time { return now }
	return h, &now
}

// poll runs one poll and requires that it read the GPUs.
func pollHealth(t *testing.T, h *Health) []string {
	t.Helper()
	conds, ok := h.Poll()
	require.True(t, ok, "the poll should have read the GPUs")
	return describe(conds)
}

func TestHealth(t *testing.T) {
	type gpuSetup func(g *fakeGPU)
	set := func(id uint32, v uint64) gpuSetup { return func(g *fakeGPU) { g.fields[id] = v } }
	fail := func(call string, ret gonvml.Return) gpuSetup { return func(g *fakeGPU) { g.fail[call] = ret } }
	unread := func(id uint32) gpuSetup { return func(g *fakeGPU) { delete(g.fields, id) } }

	cc := func(major int) gpuSetup { return func(g *fakeGPU) { g.ccMajor = major } }
	links := func(states ...gonvml.EnableState) gpuSetup {
		return func(g *fakeGPU) {
			g.links = states
			g.fields[gonvml.FI_DEV_NVLINK_LINK_COUNT] = uint64(len(states))
		}
	}
	up, down := gonvml.FEATURE_ENABLED, gonvml.FEATURE_DISABLED

	for _, tc := range []struct {
		name      string
		setup     []gpuSetup
		want      []string // reason|severity|message
		linkReads int
	}{
		{name: "a healthy GPU reports nothing"},
		{
			name:  "fields the GPU doesn't report are not observed",
			setup: []gpuSetup{func(g *fakeGPU) { g.fields = nil }},
		},
		{
			name:  "uncorrected volatile ECC errors are Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_ECC_DBE_VOL_TOTAL, 2)},
			want:  []string{"DCGMHealthCode4|Fatal|GPU 0: 2 uncorrected volatile ECC errors"},
		},
		{
			name:  "pages pending retirement are a Warning",
			setup: []gpuSetup{set(gonvml.FI_DEV_RETIRED_PENDING, 1)},
			want:  []string{"DCGMHealthCode6|Warning|GPU 0: pages are pending retirement"},
		},
		{
			name:  "63 retired pages is Fatal, and the double-bit limit isn't checked",
			setup: []gpuSetup{set(gonvml.FI_DEV_RETIRED_SBE, 40), set(gonvml.FI_DEV_RETIRED_DBE, 23)},
			want:  []string{"DCGMHealthCode7|Fatal|GPU 0: 63 pages retired (40 single-bit, 23 double-bit), at or above the limit of 63"},
		},
		{
			name:  "62 retired pages is below the limit",
			setup: []gpuSetup{set(gonvml.FI_DEV_RETIRED_SBE, 50), set(gonvml.FI_DEV_RETIRED_DBE, 12)},
		},
		{
			name:  "a single-bit count that wasn't read adds 0",
			setup: []gpuSetup{unread(gonvml.FI_DEV_RETIRED_SBE), set(gonvml.FI_DEV_RETIRED_DBE, 63)},
			want:  []string{"DCGMHealthCode7|Fatal|GPU 0: 63 pages retired (0 single-bit, 63 double-bit), at or above the limit of 63"},
		},
		{
			name:  "more than 15 double-bit retired pages is Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_RETIRED_DBE, 16)},
			want:  []string{"DCGMHealthCode8|Fatal|GPU 0: 16 pages retired after double-bit errors, above the limit of 15"},
		},
		{
			name:  "15 double-bit retired pages is at the limit",
			setup: []gpuSetup{set(gonvml.FI_DEV_RETIRED_DBE, 15)},
		},
		{
			name:  "a failed row remap is Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_REMAPPED_FAILURE, 1)},
			want:  []string{"DCGMHealthCode80|Fatal|GPU 0: row remapping failed"},
		},
		{
			name:  "512 uncorrectable row remaps on Ampere or newer is a Warning",
			setup: []gpuSetup{set(gonvml.FI_DEV_REMAPPED_UNC, 512), func(g *fakeGPU) { g.ccMajor = 8 }},
			want:  []string{"DCGMHealthCode132|Warning|GPU 0: 512 uncorrectable row remaps, at or above the limit of 512"},
		},
		{
			name:  "uncorrectable row remaps before Ampere report nothing",
			setup: []gpuSetup{set(gonvml.FI_DEV_REMAPPED_UNC, 512), func(g *fakeGPU) { g.ccMajor = 7 }},
		},
		{
			name:  "511 uncorrectable row remaps is below the limit",
			setup: []gpuSetup{set(gonvml.FI_DEV_REMAPPED_UNC, 511)},
		},
		{
			name:  "uncorrectable row remaps without a compute capability report nothing",
			setup: []gpuSetup{set(gonvml.FI_DEV_REMAPPED_UNC, 512), fail("computeCapability", gonvml.ERROR_UNKNOWN)},
		},
		{
			name:  "unrepairable memory is Fatal",
			setup: []gpuSetup{func(g *fakeGPU) { g.unrepairable = 1 }},
			want:  []string{"DCGMHealthCode52|Fatal|GPU 0: the GPU reports unrepairable memory"},
		},
		{
			name:  "a corrupted InfoROM is a Warning",
			setup: []gpuSetup{fail("inforom", gonvml.ERROR_CORRUPTED_INFOROM)},
			want:  []string{"DCGMHealthCode9|Warning|GPU 0: the InfoROM is corrupted"},
		},
		{
			name:  "an InfoROM that can't be validated is not observed",
			setup: []gpuSetup{fail("inforom", gonvml.ERROR_NOT_SUPPORTED)},
		},
		{
			name:  "power that can't be read is a Warning",
			setup: []gpuSetup{fail("power", gonvml.ERROR_UNKNOWN)},
			want:  []string{"DCGMHealthCode11|Warning|GPU 0: power usage could not be read"},
		},
		{
			name:  "a GPU that doesn't report power reports nothing",
			setup: []gpuSetup{fail("power", gonvml.ERROR_NOT_SUPPORTED)},
		},
		{
			name:  "recovery action: reset the GPU",
			setup: []gpuSetup{set(gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION, 1)},
			want:  []string{"DCGMHealthCode125|Fatal|GPU 0: NVML recovery action: reset the GPU"},
		},
		{
			name:  "recovery action: reboot the node",
			setup: []gpuSetup{set(gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION, 2)},
			want:  []string{"DCGMHealthCode126|Fatal|GPU 0: NVML recovery action: reboot the node"},
		},
		{
			name:  "recovery action: drain peer-to-peer traffic",
			setup: []gpuSetup{set(gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION, 3)},
			want:  []string{"DCGMHealthCode127|Warning|GPU 0: NVML recovery action: drain peer-to-peer traffic"},
		},
		{
			name:  "recovery action: drain and reset",
			setup: []gpuSetup{set(gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION, 4)},
			want:  []string{"DCGMHealthCode128|Fatal|GPU 0: NVML recovery action: drain peer-to-peer traffic and reset the GPU"},
		},
		{
			name:  "recovery actions newer than DCGM's are ignored",
			setup: []gpuSetup{set(gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION, 7)},
		},
		{
			name:  "an effective NVLink BER above 1e-12 is Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(2, 12))},
			want:  []string{"DCGMHealthCode119|Fatal|GPU 0: NVLink effective BER 2e-12, above 1e-12"},
		},
		{
			name:  "an effective NVLink BER of 1e-12 is at the limit",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(1, 12))},
		},
		{
			name:  "an effective NVLink BER of 10e-13 is at the limit",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(10, 13))},
		},
		{
			name:  "an effective NVLink BER of 11e-13 is Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(11, 13))},
			want:  []string{"DCGMHealthCode119|Fatal|GPU 0: NVLink effective BER 11e-13, above 1e-12"},
		},
		{
			name:  "an effective NVLink BER of 1e-11 is Fatal",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(1, 11))},
			want:  []string{"DCGMHealthCode119|Fatal|GPU 0: NVLink effective BER 1e-11, above 1e-12"},
		},
		{
			name:  "a symbol NVLink BER at the floor is healthy before Blackwell",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(9, 255))},
		},
		{
			name:  "a symbol NVLink BER of 1e-254 is Fatal before Blackwell",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(1, 254))},
			want:  []string{"DCGMHealthCode121|Fatal|GPU 0: NVLink symbol BER 1e-254, at or above 1e-254"},
		},
		{
			name:  "a symbol NVLink BER with a zero mantissa is zero",
			setup: []gpuSetup{set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(0, 100))},
		},
		{
			name:  "a symbol NVLink BER at Blackwell's floor is healthy",
			setup: []gpuSetup{cc(10), set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(15, 238))},
		},
		{
			name:  "a symbol NVLink BER of 1e-237 is Fatal on Blackwell",
			setup: []gpuSetup{cc(10), set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(1, 237))},
			want:  []string{"DCGMHealthCode121|Fatal|GPU 0: NVLink symbol BER 1e-237, at or above 1e-237"},
		},
		{
			name:  "a symbol NVLink BER that fails before Blackwell is healthy on Blackwell",
			setup: []gpuSetup{cc(10), set(gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, ber(1, 254))},
		},
		{
			name:  "NVLink5 fields aren't read without a compute capability",
			setup: []gpuSetup{fail("computeCapability", gonvml.ERROR_UNKNOWN), set(gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, ber(2, 12))},
		},
		{
			name:      "each NVLink that is down is Fatal",
			setup:     []gpuSetup{links(up, down, up, down)},
			want:      []string{"DCGMHealthCode14|Fatal|GPU 0: NVLink 1 is down", "DCGMHealthCode14|Fatal|GPU 0: NVLink 3 is down"},
			linkReads: 4,
		},
		{
			name:      "only the links the GPU reports are read",
			setup:     []gpuSetup{links(up, up, down), set(gonvml.FI_DEV_NVLINK_LINK_COUNT, 2)},
			linkReads: 2,
		},
		{
			name:  "a GPU that doesn't report its link count reads no links",
			setup: []gpuSetup{links(down), unread(gonvml.FI_DEV_NVLINK_LINK_COUNT)},
		},
		{
			name:      "a link whose state can't be read is skipped",
			setup:     []gpuSetup{links(down, down), fail("link0", gonvml.ERROR_UNKNOWN)},
			want:      []string{"DCGMHealthCode14|Fatal|GPU 0: NVLink 1 is down"},
			linkReads: 2,
		},
		{
			name:  "a GPU in MIG mode reads no links",
			setup: []gpuSetup{links(down, down), func(g *fakeGPU) { g.migMode = gonvml.DEVICE_MIG_ENABLE }},
		},
		{
			name:      "a MIG mode that can't be read counts as off",
			setup:     []gpuSetup{links(down), func(g *fakeGPU) { g.migMode = gonvml.DEVICE_MIG_ENABLE }, fail("migMode", gonvml.ERROR_UNKNOWN)},
			want:      []string{"DCGMHealthCode14|Fatal|GPU 0: NVLink 0 is down"},
			linkReads: 1,
		},
		{
			name:  "a failed NVLink recovery is Fatal on Blackwell",
			setup: []gpuSetup{cc(10), set(gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_FAILED_EVENTS, 2)},
			want:  []string{"DCGMHealthCode71|Fatal|GPU 0: failed NVLink recoveries: 2"},
		},
		{
			name:  "a successful NVLink recovery is a Warning on Blackwell",
			setup: []gpuSetup{cc(10), set(gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_SUCCESSFUL_EVENTS, 1)},
			want:  []string{"DCGMHealthCode71|Warning|GPU 0: successful NVLink recoveries: 1"},
		},
		{
			name: "NVLink recoveries before Blackwell report nothing",
			setup: []gpuSetup{
				set(gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_FAILED_EVENTS, 2),
				set(gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_SUCCESSFUL_EVENTS, 1),
			},
		},
		{
			name:  "a lost GPU reports only that it is lost",
			setup: []gpuSetup{set(gonvml.FI_DEV_ECC_DBE_VOL_TOTAL, 2), fail("fieldValues", gonvml.ERROR_GPU_IS_LOST)},
			want:  []string{"NvidiaXID79Error|Fatal|GPU 0 has fallen off the bus: NVML returned GPU_IS_LOST"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpu := newFakeGPU()
			gpu.fields = healthyFields()
			for _, s := range tc.setup {
				s(gpu)
			}
			h, _ := newHealthHarness(t, gpu)
			assert.Equal(t, tc.want, pollHealth(t, h))
			assert.Zero(t, gpu.afterLost, "no call after the first ERROR_GPU_IS_LOST")
			assert.Equal(t, tc.linkReads, gpu.linkReads, "NVLink state reads")
		})
	}
}

func TestHealth_rateRules(t *testing.T) {
	type poll struct {
		change func(g *fakeGPU) // applied before the poll
		want   []string         // reason|severity|message
	}
	nothing := func(*fakeGPU) {}
	add := func(id uint32, n uint64) func(*fakeGPU) { return func(g *fakeGPU) { g.fields[id] += n } }
	set := func(id uint32, v uint64) func(*fakeGPU) { return func(g *fakeGPU) { g.fields[id] = v } }
	const thermal, power = gonvml.FI_DEV_PERF_POLICY_THERMAL, gonvml.FI_DEV_PERF_POLICY_POWER
	const symbols, ecc = gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_ERRORS, gonvml.FI_DEV_NVLINK_ECC_DATA_ERROR_COUNT_TOTAL

	for _, tc := range []struct {
		name    string
		ccMajor int
		polls   []poll
	}{
		{
			name: "the first reading is a baseline",
			polls: []poll{
				{change: func(g *fakeGPU) { g.fields[thermal], g.fields[power], g.fields[symbols], g.fields[ecc] = 9, 9, 9, 9 }},
			},
		},
		{
			name: "thermal slowdown that grew is a rate-limited Warning",
			polls: []poll{
				// Above MaxUint32 nanoseconds, which the policy counters
				// would treat as out of range.
				{change: set(thermal, 10_000_000_000)},
				{change: add(thermal, 1_500_000_000), want: []string{
					"DCGMHealthCode10|Warning|GPU 0: clocks were slowed by a thermal limit for 1.5s since the last check",
				}},
				{change: add(thermal, 1)},
				{change: nothing},
				{change: add(thermal, 2_000_000_000), want: []string{
					"DCGMHealthCode10|Warning|GPU 0: clocks were slowed by a thermal limit for 2s since the last check",
				}},
			},
		},
		{
			name: "power slowdown that grew is a Warning",
			polls: []poll{
				{change: nothing},
				{change: add(power, 250_000_000), want: []string{
					"DCGMHealthCode12|Warning|GPU 0: clocks were slowed by a power limit for 250ms since the last check",
				}},
			},
		},
		{
			name:    "an NVLink5 error counter that grew is Fatal on every poll it grows",
			ccMajor: 10,
			polls: []poll{
				{change: nothing},
				{change: add(symbols, 3), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink symbol errors grew by 3 since the last check"}},
				{change: add(symbols, 1), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink symbol errors grew by 1 since the last check"}},
				{change: nothing},
			},
		},
		{
			name: "each NVLink counter that grew reports",
			polls: []poll{
				{change: nothing},
				{change: func(g *fakeGPU) { g.fields[symbols]++; g.fields[ecc] += 2 }, want: []string{
					"DCGMHealthCode71|Fatal|GPU 0: NVLink symbol errors grew by 1 since the last check",
					"DCGMHealthCode71|Fatal|GPU 0: NVLink ECC data errors grew by 2 since the last check",
				}},
			},
		},
		{
			name: "a counter that dropped was reset",
			polls: []poll{
				{change: set(ecc, 10)},
				{change: set(ecc, 2)},
				{change: add(ecc, 1), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink ECC data errors grew by 1 since the last check"}},
			},
		},
		{
			name: "a failed read clears the baseline",
			polls: []poll{
				{change: nothing},
				{change: func(g *fakeGPU) { g.fields[ecc]++; g.fail["field160"] = gonvml.ERROR_UNKNOWN }},
				{change: func(g *fakeGPU) { g.fields[ecc]++; delete(g.fail, "field160") }},
				{change: add(ecc, 1), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink ECC data errors grew by 1 since the last check"}},
			},
		},
		{
			// Blackwell reads the counters summed over links, and a poll
			// that can't tell the architecture doesn't read them, so a
			// per-link value never becomes a baseline for a summed one.
			name:    "a poll without a compute capability restarts the NVLink5 baselines",
			ccMajor: 10,
			polls: []poll{
				{change: set(symbols, 100)},
				{change: func(g *fakeGPU) { g.fields[symbols] = 5; g.fail["computeCapability"] = gonvml.ERROR_UNKNOWN }},
				{change: func(g *fakeGPU) { g.fields[symbols] = 100; delete(g.fail, "computeCapability") }},
				{change: add(symbols, 1), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink symbol errors grew by 1 since the last check"}},
			},
		},
		{
			name: "a lost GPU restarts its baselines",
			polls: []poll{
				{change: nothing},
				{change: func(g *fakeGPU) { g.fields[ecc]++; g.fail["handle"] = gonvml.ERROR_GPU_IS_LOST }, want: []string{
					"NvidiaXID79Error|Fatal|GPU 0 has fallen off the bus: NVML returned GPU_IS_LOST",
				}},
				{change: func(g *fakeGPU) { g.fields[ecc]++; g.lost = false; delete(g.fail, "handle") }},
				{change: add(ecc, 1), want: []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink ECC data errors grew by 1 since the last check"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpu := newFakeGPU()
			gpu.fields = healthyFields()
			if tc.ccMajor != 0 {
				gpu.ccMajor = tc.ccMajor
			}
			h, now := newHealthHarness(t, gpu)
			for i, p := range tc.polls {
				p.change(gpu)
				assert.Equal(t, p.want, pollHealth(t, h), "poll %d", i+1)
				*now = now.Add(HealthInterval)
			}
		})
	}
}

func TestHealth_unreadPollKeepsBaselines(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fields = healthyFields()
	lib := fakeLibrary(gpu)
	var h *Health
	initializedReader(t, lib, func(r *Reader) { h = NewHealth(r) })
	assert.Empty(t, pollHealth(t, h))

	count := lib.DeviceGetCountFunc
	lib.DeviceGetCountFunc = func() (int, gonvml.Return) { return 0, gonvml.ERROR_UNKNOWN }
	gpu.fields[gonvml.FI_DEV_NVLINK_ECC_DATA_ERROR_COUNT_TOTAL] = 2
	_, ok := h.Poll()
	require.False(t, ok)

	lib.DeviceGetCountFunc = count
	assert.Equal(t, []string{"DCGMHealthCode71|Fatal|GPU 0: NVLink ECC data errors grew by 2 since the last check"}, pollHealth(t, h),
		"growth during a poll that read nothing is reported by the next one")
}

func TestHealth_nvlink5Scope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ccMajor   int
		ccFails   bool
		wantScope uint32
	}{
		{name: "Blackwell asks for the NVLink5 fields summed over links", ccMajor: 10, wantScope: math.MaxUint32},
		{name: "so do GPUs newer than Blackwell", ccMajor: 12, wantScope: math.MaxUint32},
		{name: "Hopper asks for scope 0, as DCGM does", ccMajor: 9},
		{name: "without a compute capability the NVLink5 fields aren't read", ccFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpu := newFakeGPU()
			gpu.fields = healthyFields()
			gpu.ccMajor = tc.ccMajor
			if tc.ccFails {
				gpu.fail["computeCapability"] = gonvml.ERROR_UNKNOWN
			}
			h, _ := newHealthHarness(t, gpu)
			pollHealth(t, h)
			for _, f := range healthFields {
				scope, read := gpu.scopes[f.id]
				switch {
				case !f.nvlink5:
					assert.True(t, read, "field %d", f.id)
					assert.Zero(t, scope, "field %d", f.id)
				case tc.ccFails:
					assert.False(t, read, "field %d", f.id)
				default:
					assert.True(t, read, "field %d", f.id)
					assert.Equal(t, tc.wantScope, scope, "field %d", f.id)
				}
			}
		})
	}
}

func TestHealth_nvlinkDownIsMIGAware(t *testing.T) {
	plain, mig := newFakeGPU(), newFakeGPU()
	for _, g := range []*fakeGPU{plain, mig} {
		g.fields = healthyFields()
		g.links = []gonvml.EnableState{gonvml.FEATURE_DISABLED}
		g.fields[gonvml.FI_DEV_NVLINK_LINK_COUNT] = 1
	}
	mig.migMode = gonvml.DEVICE_MIG_ENABLE
	h, _ := newHealthHarness(t, plain, mig)

	assert.Empty(t, pollHealth(t, h), "no link counts as down while any GPU is in MIG mode")
	assert.Equal(t, 1, plain.linkReads)
	assert.Zero(t, mig.linkReads, "a GPU in MIG mode reads no links")

	mig.migMode = 0
	assert.Equal(t, []string{
		"DCGMHealthCode14|Fatal|GPU 0: NVLink 0 is down",
		"DCGMHealthCode14|Fatal|GPU 1: NVLink 0 is down",
	}, pollHealth(t, h))
}

func TestHealth_namesTheGPU(t *testing.T) {
	healthy, sick := newFakeGPU(), newFakeGPU()
	healthy.fields, sick.fields = healthyFields(), healthyFields()
	sick.uuid, sick.serial = "GPU-bbbb", "1320521012345"
	sick.fields[gonvml.FI_DEV_REMAPPED_FAILURE] = 1
	h, _ := newHealthHarness(t, healthy, sick)
	assert.Equal(t, []string{
		"DCGMHealthCode80|Fatal|GPU 1 (UUID GPU-bbbb, serial 1320521012345): row remapping failed",
	}, pollHealth(t, h))
}

func TestHealth_warningsAreRateLimited(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fields = healthyFields()
	gpu.fields[gonvml.FI_DEV_RETIRED_PENDING] = 1
	h, now := newHealthHarness(t, gpu)
	pending := []string{"DCGMHealthCode6|Warning|GPU 0: pages are pending retirement"}

	assert.Equal(t, pending, pollHealth(t, h))
	*now = now.Add(HealthInterval)
	assert.Empty(t, pollHealth(t, h), "a repeat inside the re-notify interval is dropped")
	*now = now.Add(30 * time.Minute)
	assert.Equal(t, pending, pollHealth(t, h), "the Warning repeats once the interval has passed")

	gpu.fields[gonvml.FI_DEV_RETIRED_PENDING] = 0
	assert.Empty(t, pollHealth(t, h))
	gpu.fields[gonvml.FI_DEV_RETIRED_PENDING] = 1
	assert.Equal(t, pending, pollHealth(t, h), "after a clean read the next Warning reports at once")

	delete(gpu.fields, gonvml.FI_DEV_RETIRED_PENDING)
	assert.Empty(t, pollHealth(t, h))
	gpu.fields[gonvml.FI_DEV_RETIRED_PENDING] = 1
	assert.Empty(t, pollHealth(t, h), "a read that wasn't observed is not clean, so the limit still holds")
}

func TestHealth_fatalsReportEveryPoll(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fields = healthyFields()
	gpu.fields[gonvml.FI_DEV_ECC_DBE_VOL_TOTAL] = 1
	h, now := newHealthHarness(t, gpu)
	fatal := []string{"DCGMHealthCode4|Fatal|GPU 0: 1 uncorrected volatile ECC errors"}
	assert.Equal(t, fatal, pollHealth(t, h))
	*now = now.Add(HealthInterval)
	assert.Equal(t, fatal, pollHealth(t, h))
}

func TestHealth_inforomValidatedHourly(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fields = healthyFields()
	gpu.fail["inforom"] = gonvml.ERROR_CORRUPTED_INFOROM
	h, now := newHealthHarness(t, gpu)
	corrupt := []string{"DCGMHealthCode9|Warning|GPU 0: the InfoROM is corrupted"}

	assert.Equal(t, corrupt, pollHealth(t, h))
	assert.Equal(t, 1, gpu.inforomReads)

	*now = now.Add(35 * time.Minute)
	assert.Equal(t, corrupt, pollHealth(t, h), "between validations the last result is reported")
	assert.Equal(t, 1, gpu.inforomReads, "no validation inside the hour")

	delete(gpu.fail, "inforom")
	*now = now.Add(25 * time.Minute)
	assert.Empty(t, pollHealth(t, h))
	assert.Equal(t, 2, gpu.inforomReads, "a validation once the hour has passed")
}

func TestHealth_moreGPUsThanTheLastInforomValidation(t *testing.T) {
	first, added := newFakeGPU(), newFakeGPU()
	first.fields, added.fields = healthyFields(), healthyFields()
	added.fail["inforom"] = gonvml.ERROR_CORRUPTED_INFOROM
	lib := fakeLibrary(first, added)
	count := 1
	lib.DeviceGetCountFunc = func() (int, gonvml.Return) { return count, gonvml.SUCCESS }
	var h *Health
	initializedReader(t, lib, func(r *Reader) { h = NewHealth(r) })
	now := time.Unix(0, 0)
	h.now = func() time.Time { return now }

	assert.Empty(t, pollHealth(t, h), "the first poll validates one GPU")

	count = 2
	now = now.Add(HealthInterval)
	assert.Empty(t, pollHealth(t, h), "a GPU the last validation didn't see reads its InfoROM as not observed")
	assert.Zero(t, added.inforomReads)

	now = now.Add(inforomInterval)
	assert.Equal(t, []string{"DCGMHealthCode9|Warning|GPU 1: the InfoROM is corrupted"}, pollHealth(t, h),
		"the next validation reads it")
}

func TestHealth_unrepairableMemoryNeedsTheSymbol(t *testing.T) {
	gpu := newFakeGPU()
	gpu.fields = healthyFields()
	gpu.unrepairable = 1
	lib := fakeLibrary(gpu)
	lib.ExtensionsFunc = func() gonvml.ExtendedInterface {
		return &mock.ExtendedInterface{LookupSymbolFunc: func(name string) error {
			if name == "nvmlDeviceGetUnrepairableMemoryFlag_v1" {
				return errors.New("undefined symbol")
			}
			return nil
		}}
	}
	var h *Health
	initializedReader(t, lib, func(r *Reader) { h = NewHealth(r) })
	assert.Empty(t, pollHealth(t, h))
	assert.Zero(t, gpu.unrepairableReads, "calling a symbol the driver lacks would abort the process")
}

func TestHealth_unknown(t *testing.T) {
	t.Run("NVML not initialized", func(t *testing.T) {
		r := newReader(Config{}, time.Now, func() []string { return nil }, nil)
		conds, ok := NewHealth(r).Poll()
		assert.Empty(t, conds)
		assert.False(t, ok)
	})

	t.Run("GPU count unreadable", func(t *testing.T) {
		gpu := newFakeGPU()
		lib := fakeLibrary(gpu)
		var h *Health
		initializedReader(t, lib, func(r *Reader) { h = NewHealth(r) })
		lib.DeviceGetCountFunc = func() (int, gonvml.Return) { return 0, gonvml.ERROR_UNKNOWN }
		conds, ok := h.Poll()
		assert.Empty(t, conds)
		assert.False(t, ok)
	})

	t.Run("stuck in the driver", func(t *testing.T) {
		gpu := newFakeGPU()
		gpu.fields = healthyFields()
		h, _ := newHealthHarness(t, gpu)
		h.check.timeout = 20 * time.Millisecond
		release := make(chan struct{})
		h.reader.ready.Load().lib.(*mock.Interface).DeviceGetCountFunc = func() (int, gonvml.Return) {
			<-release
			return 1, gonvml.SUCCESS
		}
		for _, msg := range []string{"the stuck poll", "the next poll, which skips while the call is in the driver"} {
			conds, ok := h.Poll()
			assert.Empty(t, conds, msg)
			assert.False(t, ok, "%s is unknown, not clean", msg)
		}
		assert.NotZero(t, h.check.inDriverSince.Load(), "the stuck call is still in the driver")
		assert.True(t, h.inforomAt.IsZero(), "a validation that didn't run stays due")
		close(release)
		select {
		case <-h.check.pending:
		case <-time.After(time.Second):
			t.Fatal("the released call never returned")
		}
	})
}
