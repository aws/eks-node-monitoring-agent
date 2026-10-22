package nvml

import (
	"errors"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthyFields are the health field values of a GPU with nothing wrong.
func healthyFields() map[uint32]uint64 {
	fields := map[uint32]uint64{}
	for _, id := range healthFieldIDs {
		fields[id] = 0
	}
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

	for _, tc := range []struct {
		name  string
		setup []gpuSetup
		want  []string // reason|severity|message
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
		})
	}
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
