package nvml

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		ret        gonvml.Return
		want       Outcome
		unexpected bool
	}{
		{gonvml.SUCCESS, Observed, false},
		{gonvml.ERROR_NOT_SUPPORTED, NotObserved, false},
		{gonvml.ERROR_FUNCTION_NOT_FOUND, FunctionMissing, false},
		{gonvml.ERROR_GPU_IS_LOST, GPULost, false},
		{gonvml.ERROR_UNKNOWN, Unexpected, true},
		{gonvml.ERROR_TIMEOUT, Unexpected, true},
		{gonvml.ERROR_NO_PERMISSION, Unexpected, true},
		{gonvml.ERROR_UNINITIALIZED, Unexpected, true},
	} {
		t.Run(strconv.Itoa(int(tc.ret)), func(t *testing.T) {
			counter := unexpectedReturns.WithLabelValues("testCall", strconv.Itoa(int(tc.ret)))
			before := testutil.ToFloat64(counter)
			assert.Equal(t, tc.want, outcome("testCall", tc.ret))
			want := before
			if tc.unexpected {
				want++
			}
			assert.Equal(t, want, testutil.ToFloat64(counter), "nvml_unexpected_returns_total")
		})
	}
}

func TestSymbols(t *testing.T) {
	ext := &mock.ExtendedInterface{LookupSymbolFunc: func(name string) error {
		if name == "present" {
			return nil
		}
		return errors.New("undefined symbol")
	}}
	syms := lookupSymbols(ext, []string{"present", "missing"})
	assert.True(t, syms.Has("present"))
	assert.False(t, syms.Has("missing"))
	assert.False(t, syms.Has("neverLookedUp"), "a name outside the list must fail closed")
	assert.Len(t, ext.LookupSymbolCalls(), 2, "each name is looked up once")
}

func TestRun(t *testing.T) {
	c := &Check{name: "test", timeout: 20 * time.Millisecond}

	v, status := run(c, func() int { return 7 })
	assert.Equal(t, Done, status)
	assert.Equal(t, 7, v)

	// A call that outlives its deadline reports Stuck and keeps running.
	release := make(chan struct{})
	var calls atomic.Int32
	hang := func() int { calls.Add(1); <-release; return 1 }
	v, status = run(c, hang)
	assert.Equal(t, Stuck, status)
	assert.Zero(t, v)
	assert.Equal(t, 1, c.StuckCycles())

	// While it is outstanding, later runs make no new call.
	for i := 2; i <= stuckThreshold; i++ {
		_, status = run(c, hang)
		assert.Equal(t, Stuck, status)
		assert.Equal(t, i, c.StuckCycles())
	}
	assert.Equal(t, int32(1), calls.Load(), "only one call may be in the driver at a time")

	// Once the driver answers, the next run makes a new call and clears the count.
	close(release)
	assert.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		select {
		case <-c.pending:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	v, status = run(c, func() int { return 9 })
	assert.Equal(t, Done, status)
	assert.Equal(t, 9, v)
	assert.Zero(t, c.StuckCycles())
}

func TestAllStuck(t *testing.T) {
	stuck := func(n int) *Check { return &Check{stuckCycles: n} }
	assert.False(t, allStuck(nil), "no checks is not a hung driver")
	assert.False(t, allStuck([]*Check{stuck(stuckThreshold), stuck(stuckThreshold - 1)}))
	assert.True(t, allStuck([]*Check{stuck(stuckThreshold), stuck(stuckThreshold + 3)}))
}

func TestLibraryCandidates(t *testing.T) {
	touch := func(t *testing.T, root, path string) string {
		t.Helper()
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, nil, 0o644))
		return full
	}

	t.Run("host process tries the soname first", func(t *testing.T) {
		t.Setenv(config.HOST_ROOT_ENV, "/")
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "")
		got := libraryCandidates()
		require.NotEmpty(t, got)
		assert.Equal(t, libraryName, got[0])
	})

	t.Run("container loads only from the host root", func(t *testing.T) {
		host := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, host)
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "")
		ubuntu := touch(t, host, "usr/lib/x86_64-linux-gnu/"+libraryName)
		al := touch(t, host, "usr/lib64/"+libraryName)
		require.NoError(t, os.MkdirAll(filepath.Join(host, "usr/lib/aarch64-linux-gnu", libraryName), 0o755)) // a directory is not a library
		assert.Equal(t, []string{al, ubuntu}, libraryCandidates())
	})

	t.Run("GPU Operator driver root comes first", func(t *testing.T) {
		host := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, host)
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "")
		hostLib := touch(t, host, "usr/lib64/"+libraryName)
		operatorLib := touch(t, host, "run/nvidia/driver/usr/lib/x86_64-linux-gnu/"+libraryName)
		assert.Equal(t, []string{operatorLib, hostLib}, libraryCandidates())
	})

	t.Run("override is the only driver root", func(t *testing.T) {
		host := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, host)
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "/opt/driver")
		touch(t, host, "usr/lib64/"+libraryName)
		touch(t, host, "usr/share/.nvidia/lts/lib64/"+libraryName)
		custom := touch(t, host, "opt/driver/usr/lib64/"+libraryName)
		assert.Equal(t, []string{custom}, libraryCandidates())
	})

	t.Run("Bottlerocket branch roots", func(t *testing.T) {
		host := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, host)
		t.Setenv(config.NVIDIA_DRIVER_ROOT_ENV, "")
		lts := touch(t, host, "usr/share/.nvidia/lts/lib64/"+libraryName)
		assert.Equal(t, []string{lts}, libraryCandidates())
	})
}

func TestOpenLibrary(t *testing.T) {
	results := map[string]gonvml.Return{
		"missing":   gonvml.ERROR_LIBRARY_NOT_FOUND,
		"nodriver":  gonvml.ERROR_DRIVER_NOT_LOADED,
		"good":      gonvml.SUCCESS,
		"neverUsed": gonvml.SUCCESS,
	}
	var tried []string
	newLibrary := func(path string) gonvml.Interface {
		return &mock.Interface{InitFunc: func() gonvml.Return { tried = append(tried, path); return results[path] }}
	}

	lib, path, ret := openLibrary([]string{"missing", "good", "neverUsed"}, newLibrary)
	assert.NotNil(t, lib)
	assert.Equal(t, "good", path)
	assert.Equal(t, gonvml.SUCCESS, ret)
	assert.Equal(t, []string{"missing", "good"}, tried)

	// A library that loads but whose driver is missing ends the search: the
	// library was found, and another copy would not load the driver either.
	tried = nil
	lib, path, ret = openLibrary([]string{"nodriver", "good"}, newLibrary)
	assert.NotNil(t, lib)
	assert.Equal(t, "nodriver", path)
	assert.Equal(t, gonvml.ERROR_DRIVER_NOT_LOADED, ret)
	assert.Equal(t, []string{"nodriver"}, tried)

	lib, _, ret = openLibrary([]string{"missing"}, newLibrary)
	assert.Nil(t, lib)
	assert.Equal(t, gonvml.ERROR_LIBRARY_NOT_FOUND, ret)
	lib, _, ret = openLibrary(nil, newLibrary)
	assert.Nil(t, lib)
	assert.Equal(t, gonvml.ERROR_LIBRARY_NOT_FOUND, ret)
}

// testReader returns a Reader on a fake clock whose only candidate library
// returns init from Init and exports every symbol.
func testReader(init func() gonvml.Return) (*Reader, *time.Time) {
	now := time.Unix(0, 0)
	lib := &mock.Interface{
		InitFunc: init,
		ExtensionsFunc: func() gonvml.ExtendedInterface {
			return &mock.ExtendedInterface{LookupSymbolFunc: func(string) error { return nil }}
		},
	}
	r := newReader(Config{GracePeriod: time.Minute}, func() time.Time { return now },
		func() []string { return []string{"/host/usr/lib64/" + libraryName} },
		func(string) gonvml.Interface { return lib })
	return r, &now
}

func assertSingle(t *testing.T, got []monitor.Condition, reason string, sev monitor.Severity) {
	t.Helper()
	require.Len(t, got, 1, "conditions: %+v", got)
	assert.Equal(t, reason, got[0].Reason)
	assert.Equal(t, sev, got[0].Severity)
}

func TestReconcile_libraryNotFound(t *testing.T) {
	r, now := testReader(func() gonvml.Return { return gonvml.ERROR_LIBRARY_NOT_FOUND })
	ctx := t.Context()

	assert.Empty(t, r.Reconcile(ctx), "silent inside the grace period")
	assert.Equal(t, float64(0), testutil.ToFloat64(libraryLoaded))

	*now = now.Add(time.Minute)
	assertSingle(t, r.Reconcile(ctx), "NvidiaLibraryNotFound", monitor.SeverityWarning)

	*now = now.Add(30 * time.Second)
	assert.Empty(t, r.Reconcile(ctx), "rate-limited")

	*now = now.Add(classify.WarningReNotifyInterval)
	assertSingle(t, r.Reconcile(ctx), "NvidiaLibraryNotFound", monitor.SeverityWarning)

	_, _, ok := r.Library()
	assert.False(t, ok)
}

func TestReconcile_driverNotLoaded(t *testing.T) {
	ret := gonvml.ERROR_DRIVER_NOT_LOADED
	r, now := testReader(func() gonvml.Return { return ret })
	ctx := t.Context()

	assert.Empty(t, r.Reconcile(ctx), "silent inside the grace period")
	assert.Equal(t, float64(1), testutil.ToFloat64(libraryLoaded))

	*now = now.Add(time.Minute)
	assertSingle(t, r.Reconcile(ctx), "DCGMError", monitor.SeverityFatal)

	// The driver loads. The next Reconcile initializes on the same library.
	ret = gonvml.SUCCESS
	assert.Empty(t, r.Reconcile(ctx))
	lib, syms, ok := r.Library()
	assert.True(t, ok)
	assert.NotNil(t, lib)
	assert.True(t, syms.Has("nvmlDeviceGetGpuFabricInfoV"))
	assert.Empty(t, r.Reconcile(ctx), "an initialized reader with no checks reports nothing")
}

func TestReconcile_initHangs(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r, now := testReader(func() gonvml.Return { <-release; return gonvml.SUCCESS })
	r.initCheck.timeout = 10 * time.Millisecond
	ctx := t.Context()
	*now = now.Add(time.Minute)

	for i := 1; i < stuckThreshold; i++ {
		assert.Empty(t, r.Reconcile(ctx), "run %d", i)
	}
	assertSingle(t, r.Reconcile(ctx), "DCGMError", monitor.SeverityFatal)
}

func TestReconcile_everyCheckStuck(t *testing.T) {
	r, _ := testReader(func() gonvml.Return { return gonvml.SUCCESS })
	ctx := t.Context()
	assert.Empty(t, r.Reconcile(ctx))

	a := r.NewCheck("a", DefaultTimeout)
	b := r.NewCheck("b", DefaultTimeout)
	a.stuckCycles, b.stuckCycles = stuckThreshold, stuckThreshold-1
	assert.Empty(t, r.Reconcile(ctx), "one check still answering means the driver is not hung")

	b.stuckCycles = stuckThreshold
	assertSingle(t, r.Reconcile(ctx), "DCGMError", monitor.SeverityFatal)
}
