package nvml

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

// escalationHarness is an initialized Reader with two registered checks that
// read the GPUs, two GPUs whose second one wedges the driver probe until
// release, a fake clock and a fake host memory pressure.
type escalationHarness struct {
	r           *Reader
	now         *time.Time
	checks      []*Check
	wedge       chan struct{}
	reads       atomic.Int32 // probe reads made
	pressure    float64
	pressureErr error
}

func newEscalationHarness(t *testing.T) *escalationHarness {
	t.Helper()
	h := &escalationHarness{wedge: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-h.wedge:
		default:
			close(h.wedge)
		}
	})
	pcie := func(wedged bool) func() (int, gonvml.Return) {
		return func() (int, gonvml.Return) {
			h.reads.Add(1)
			if wedged {
				<-h.wedge
			}
			return 0, gonvml.SUCCESS
		}
	}
	gpus := []gonvml.Device{
		&mock.Device{GetPcieReplayCounterFunc: pcie(false)},
		&mock.Device{GetPcieReplayCounterFunc: pcie(true)},
	}
	lib := &mock.Interface{
		InitFunc: func() gonvml.Return { return gonvml.SUCCESS },
		ExtensionsFunc: func() gonvml.ExtendedInterface {
			return &mock.ExtendedInterface{LookupSymbolFunc: func(string) error { return nil }}
		},
		DeviceGetCountFunc:         func() (int, gonvml.Return) { return len(gpus), gonvml.SUCCESS },
		DeviceGetHandleByIndexFunc: func(i int) (gonvml.Device, gonvml.Return) { return gpus[i], gonvml.SUCCESS },
	}
	now := time.Unix(0, 0)
	h.now = &now
	h.r = newReader(Config{}, func() time.Time { return *h.now },
		func() []string { return []string{"/host/usr/lib64/" + libraryName} },
		func(string) gonvml.Interface { return lib })
	h.r.probe.timeout = 20 * time.Millisecond
	h.r.memoryPressure = func() (float64, error) { return h.pressure, h.pressureErr }
	h.checks = []*Check{h.r.NewCheck("policies", DefaultTimeout), h.r.NewCheck("devices", DefaultTimeout)}
	require.Empty(t, h.r.Reconcile(t.Context()))
	return h
}

// stall marks the checks that read the GPUs stuck for stuckThreshold runs.
func (h *escalationHarness) stall() {
	for _, c := range h.checks {
		c.stuck.Store(stuckThreshold)
	}
}

func (h *escalationHarness) unstall() {
	for _, c := range h.checks {
		c.stuck.Store(0)
	}
}

// release lets the wedged probe call return, and waits until it has.
func (h *escalationHarness) release(t *testing.T) {
	t.Helper()
	close(h.wedge)
	select {
	case <-h.r.probe.pending:
	case <-time.After(time.Second):
		t.Fatal("the released probe never returned")
	}
}

// reasonsOf lists the reasons of conds, in order.
func reasonsOf(conds []monitor.Condition) []string {
	var out []string
	for _, c := range conds {
		out = append(out, c.Reason)
	}
	return out
}

func TestEscalate(t *testing.T) {
	h := newEscalationHarness(t)
	ctx := t.Context()
	h.stall()

	assertSingle(t, h.r.Reconcile(ctx), "NvidiaDriverNotResponding", monitor.SeverityWarning)
	assert.Equal(t, int32(2), h.reads.Load(), "the probe reads every GPU and stays stuck on the wedged one")

	*h.now = h.now.Add(escalationWindow - time.Second)
	assert.Empty(t, h.r.Reconcile(ctx), "stuck for less than the window")
	assert.Equal(t, int32(2), h.reads.Load(), "a probe still in the driver is not repeated")

	*h.now = h.now.Add(time.Second)
	got := h.r.Reconcile(ctx)
	assertSingle(t, got, "NvidiaDriverUnresponsive", monitor.SeverityFatal)
	assert.Equal(t, "a light driver read has been stuck in the NVIDIA driver for at least 15m0s after the checks that read the GPUs stopped answering", got[0].Message)
	assertSingle(t, h.r.Reconcile(ctx), "NvidiaDriverUnresponsive", monitor.SeverityFatal)

	// The probe answers, so the driver is slow, not hung, and the window
	// starts over.
	h.release(t)
	assert.Empty(t, h.r.Reconcile(ctx))
	*h.now = h.now.Add(escalationWindow)
	assert.NotContains(t, reasonsOf(h.r.Reconcile(ctx)), "NvidiaDriverUnresponsive", "a probe that answers never escalates")
}

func TestEscalate_hostMemoryPressure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pressure float64
		err      error
		fatal    bool
	}{
		{name: "low pressure escalates", pressure: 2.5, fatal: true},
		{name: "pressure at the limit escalates", pressure: maxHostMemoryPressure, fatal: true},
		{name: "pressure above the limit holds at Warning", pressure: maxHostMemoryPressure + 0.01},
		{name: "unreadable pressure holds at Warning", err: os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEscalationHarness(t)
			ctx := t.Context()
			h.pressure, h.pressureErr = tc.pressure, tc.err
			h.stall()
			h.r.Reconcile(ctx)
			*h.now = h.now.Add(escalationWindow)

			if tc.fatal {
				assertSingle(t, h.r.Reconcile(ctx), "NvidiaDriverUnresponsive", monitor.SeverityFatal)
				return
			}
			assert.Empty(t, h.r.Reconcile(ctx))
			assert.Empty(t, h.r.Reconcile(ctx))
			// Pressure drops while the probe is still stuck: report at once.
			h.pressure, h.pressureErr = 0, nil
			assertSingle(t, h.r.Reconcile(ctx), "NvidiaDriverUnresponsive", monitor.SeverityFatal)
		})
	}
}

func TestEscalate_checksRecoverRestartsWindow(t *testing.T) {
	h := newEscalationHarness(t)
	ctx := t.Context()
	h.stall()
	h.r.Reconcile(ctx)

	*h.now = h.now.Add(escalationWindow - time.Minute)
	h.unstall()
	assert.Empty(t, h.r.Reconcile(ctx), "the checks answer again")

	h.stall()
	*h.now = h.now.Add(time.Minute) // the old window would have ended here
	assert.Equal(t, []string{"NvidiaDriverNotResponding"}, reasonsOf(h.r.Reconcile(ctx)),
		"a new stall reports the Warning at once and starts a new window")
	assert.Equal(t, int32(2), h.reads.Load(), "the probe call from the first stall is still in the driver")

	*h.now = h.now.Add(escalationWindow)
	assertSingle(t, h.r.Reconcile(ctx), "NvidiaDriverUnresponsive", monitor.SeverityFatal)
}

// TestStallCountsChecksThatReadGPUs checks that the XID wait doesn't count
// toward the stall: it waits on the event set, so it can keep returning while
// one GPU is wedged and the checks that read every GPU are stuck.
func TestStallCountsChecksThatReadGPUs(t *testing.T) {
	r, _ := testReader(func() gonvml.Return { return gonvml.SUCCESS })
	p := NewPolicies(r)
	d := NewDevices(r, nil)
	NewXIDs(r)
	assert.ElementsMatch(t, []*Check{p.check, d.check}, r.checks)

	ctx := t.Context()
	require.Empty(t, r.Reconcile(ctx))
	p.check.stuck.Store(stuckThreshold)
	d.check.stuck.Store(stuckThreshold)
	assertSingle(t, r.Reconcile(ctx), "NvidiaDriverNotResponding", monitor.SeverityWarning)
}

func TestFullAvg60(t *testing.T) {
	for _, tc := range []struct {
		name string
		psi  string
		want float64
		ok   bool
	}{
		{
			name: "kernel format",
			psi:  "some avg10=1.50 avg60=2.25 avg300=0.10 total=123\nfull avg10=0.50 avg60=12.75 avg300=0.05 total=45\n",
			want: 12.75,
			ok:   true,
		},
		{name: "no full line", psi: "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"},
		{name: "malformed value", psi: "full avg10=0.00 avg60=high avg300=0.00 total=0\n"},
		{name: "empty", psi: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fullAvg60(tc.psi)
			if !tc.ok {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHostMemoryPressure(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.HOST_ROOT_ENV, root)
	_, err := hostMemoryPressure()
	assert.ErrorIs(t, err, os.ErrNotExist, "a kernel with PSI off has no file")

	dir := filepath.Join(root, "proc", "pressure")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory"),
		[]byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=29\nfull avg10=0.00 avg60=3.10 avg300=0.00 total=29\n"), 0o644))
	got, err := hostMemoryPressure()
	require.NoError(t, err)
	assert.Equal(t, 3.10, got)
}
