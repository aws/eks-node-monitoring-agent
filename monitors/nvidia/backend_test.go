//go:build !darwin

package nvidia

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/nvml"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

func TestSelectBackend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		value       string
		set         bool
		want        backend
		wantUnknown bool
	}{
		{name: "unset is the default", want: defaultBackend},
		{name: "empty is the default", set: true, want: defaultBackend},
		{name: "dcgm", value: "dcgm", set: true, want: backendDCGM},
		{name: "nvml", value: "nvml", set: true, want: backendNVML},
		{name: "an unknown value is the default, and reported", value: "shadow", set: true, want: defaultBackend, wantUnknown: true},
		{name: "values are case-sensitive", value: "NVML", set: true, want: defaultBackend, wantUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, unknown := selectBackend(tc.value, tc.set)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantUnknown, unknown)
		})
	}
	assert.Equal(t, backendDCGM, defaultBackend, "this release reads through DCGM unless the backend is overridden")
}

// countingDCGM counts the DCGM client's reconciles, which the monitor's
// goroutines make.
type countingDCGM struct {
	fake.FakeDcgm
	reconciles atomic.Int32
}

func newCountingDCGM() *countingDCGM {
	return &countingDCGM{FakeDcgm: fake.FakeDcgm{PolicyChan: make(chan dcgmapi.PolicyViolation)}}
}

func (d *countingDCGM) Reconcile(ctx context.Context) (bool, error) {
	d.reconciles.Add(1)
	return d.FakeDcgm.Reconcile(ctx)
}

// recordingManager collects every condition the monitor sends.
type recordingManager struct {
	obs   observer.BaseObserver
	conds chan monitor.Condition
}

func (m *recordingManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), nil
}

func (m *recordingManager) Notify(ctx context.Context, c monitor.Condition) error {
	select {
	case m.conds <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fastTicks sends each interval the monitor asks for on intervals, and ticks
// every few milliseconds instead.
type fastTicks struct {
	intervals chan time.Duration
}

func newFastTicks() *fastTicks {
	return &fastTicks{intervals: make(chan time.Duration, 16)}
}

func (f *fastTicks) tick(ctx context.Context, d time.Duration) <-chan time.Time {
	f.intervals <- d
	ticker := time.NewTicker(5 * time.Millisecond)
	go func() {
		<-ctx.Done()
		ticker.Stop()
	}()
	return ticker.C
}

// asked returns the intervals asked for so far.
func (f *fastTicks) asked() []time.Duration {
	var out []time.Duration
	for {
		select {
		case d := <-f.intervals:
			out = append(out, d)
		default:
			return out
		}
	}
}

func TestRegister_nvml(t *testing.T) {
	t.Setenv(backendEnv, "nvml")
	dcgmClient := newCountingDCGM()
	ticks := newFastTicks()
	info := fakeInstanceInfo{}
	m := NewNvidiaMonitorWithDeps(dcgmClient, ticks.tick, info)
	m.SetDCGMPowerThresholdWatts(900)

	cond := func(reason string) monitor.Condition {
		return monitor.Condition{Reason: reason, Severity: monitor.SeverityWarning}
	}
	var got nvmlConfig
	m.newNVMLChecks = func(cfg nvmlConfig) nvmlChecks {
		got = cfg
		return nvmlChecks{
			reconcile: func(context.Context) []monitor.Condition { return []monitor.Condition{cond("FromReconcile")} },
			policies:  func() ([]monitor.Condition, bool) { return []monitor.Condition{cond("FromPolicies")}, true },
			devices: func(context.Context) ([]monitor.Condition, bool) {
				return []monitor.Condition{cond("FromDevices")}, true
			},
			health: func() ([]monitor.Condition, bool) { return []monitor.Condition{cond("FromHealth")}, true },
			xids: func(ctx context.Context) ([]monitor.Condition, bool) {
				// Like the real wait, this blocks for a while before
				// returning, so the loop doesn't spin.
				select {
				case <-time.After(5 * time.Millisecond):
				case <-ctx.Done():
				}
				return []monitor.Condition{cond("FromXIDs")}, true
			},
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mgr := &recordingManager{conds: make(chan monitor.Condition, 64)}
	require.NoError(t, m.Register(ctx, mgr))

	want := map[string]bool{"FromReconcile": true, "FromPolicies": true, "FromDevices": true, "FromHealth": true, "FromXIDs": true}
	deadline := time.After(5 * time.Second)
	for len(want) > 0 {
		select {
		case c := <-mgr.conds:
			delete(want, c.Reason)
		case <-deadline:
			t.Fatalf("conditions never sent from: %v", want)
		}
	}
	cancel()

	assert.Equal(t, nvmlConfig{gracePeriod: dcgmClientInializationGracePeriod, powerThresholdWatts: 900, instanceInfo: info}, got,
		"the NVML checks get the grace period, the configured power threshold and the instance type lookup")
	assert.ElementsMatch(t, []time.Duration{30 * time.Second, nvml.PolicyInterval, nvml.DevicesInterval, nvml.HealthInterval}, ticks.asked(),
		"each NVML check runs at its own interval")
	assert.Zero(t, dcgmClient.reconciles.Load(), "the DCGM client is never started")
}

func TestRegister_dcgmIsTheDefault(t *testing.T) {
	for _, value := range []string{"", "dcgm", "unknown"} {
		t.Run("NMA_NVIDIA_BACKEND="+value, func(t *testing.T) {
			t.Setenv(backendEnv, value)
			dcgmClient := newCountingDCGM()
			ticks := newFastTicks()
			m := NewNvidiaMonitorWithDeps(dcgmClient, ticks.tick, fakeInstanceInfo{})
			m.newNVMLChecks = func(nvmlConfig) nvmlChecks {
				t.Fatal("the NVML checks are created only when the backend is nvml")
				return nvmlChecks{}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mgr := &recordingManager{conds: make(chan monitor.Condition, 64)}
			require.NoError(t, m.Register(ctx, mgr))
			assert.Eventually(t, func() bool { return dcgmClient.reconciles.Load() > 0 }, 5*time.Second, 5*time.Millisecond,
				"the DCGM client reconciles")
		})
	}
}

func TestNewNVMLChecks(t *testing.T) {
	checks := newNVMLChecks(nvmlConfig{gracePeriod: time.Minute, powerThresholdWatts: 900})
	// Before the reader has initialized NVML, every poll reads nothing.
	for name, poll := range map[string]func() ([]monitor.Condition, bool){
		"policies": checks.policies,
		"devices":  func() ([]monitor.Condition, bool) { return checks.devices(t.Context()) },
		"health":   checks.health,
	} {
		conds, ok := poll()
		assert.Empty(t, conds, name)
		assert.False(t, ok, "%s reads nothing before NVML initializes", name)
	}
	assert.NotNil(t, checks.reconcile)
	assert.NotNil(t, checks.xids)
}

type fakeInstanceInfo struct{}

func (fakeInstanceInfo) GetInstanceInfo(context.Context) (*instanceinfo.InstanceInfo, error) {
	return &instanceinfo.InstanceInfo{InstanceType: "test"}, nil
}
