package nvml

import (
	"context"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
)

// xidWaitTimeout is how long one Wait waits in the driver for an event. The
// wait is bounded, so it is not a stuck call, and it stays well under
// DefaultTimeout.
const xidWaitTimeout = 5 * time.Second

// policyRepeatFloor is the shortest time between two reports of the same
// policy for the same GPU. DCGM drops any repeat inside this window.
const policyRepeatFloor = 5 * time.Second

// XIDs receives critical XID events from every GPU through one NVML event
// set, the same events DCGM's XID policy reads.
type XIDs struct {
	reader *Reader
	check  *Check
	now    func() time.Time
	sleep  func(context.Context, time.Duration)

	// set, devices and lastSent are only touched by the goroutine that calls
	// Wait. devices maps each registered GPU to its index.
	set      gonvml.EventSet
	devices  map[gonvml.Device]int
	lastSent map[int]time.Time
}

// NewXIDs registers the XID check with r. Create it before the first call to
// r.Reconcile.
func NewXIDs(r *Reader) *XIDs {
	return &XIDs{
		reader:   r,
		check:    r.NewCheck("xid", DefaultTimeout),
		now:      time.Now,
		sleep:    sleepContext,
		lastSent: map[int]time.Time{},
	}
}

// Wait waits for the next XID event and returns its condition, or nothing if
// none arrives within about five seconds. ok is false when it could not wait
// in the driver: NVML is not initialized, registration failed, a call is
// stuck, or the wait failed. Wait pauses before returning in those cases, so
// a caller can loop on it without spinning. A false ok is unknown, not a
// quiet period. Call it from one goroutine.
func (x *XIDs) Wait(ctx context.Context) (conds []monitor.Condition, ok bool) {
	if x.set == nil && !x.register() {
		x.sleep(ctx, xidWaitTimeout)
		return nil, false
	}

	type event struct {
		data gonvml.EventData
		ret  gonvml.Return
	}
	set := x.set
	ev, status := run(x.check, func() event {
		data, ret := set.Wait(uint32(xidWaitTimeout / time.Millisecond))
		return event{data, ret}
	})
	switch {
	case status == Stuck:
		x.sleep(ctx, xidWaitTimeout)
		return nil, false
	case ev.ret == gonvml.ERROR_TIMEOUT:
		return nil, true
	case outcome("nvmlEventSetWait", ev.ret) != Observed:
		x.sleep(ctx, xidWaitTimeout)
		return nil, false
	case ev.data.EventType&gonvml.EventTypeXidCriticalError == 0:
		return nil, true
	}

	// MIG instance IDs are ignored, because conditions are per node.
	gpu, known := x.devices[ev.data.Device]
	if !known {
		gpu = -1
	}
	now := x.now()
	if last, seen := x.lastSent[gpu]; seen && now.Sub(last) < policyRepeatFloor {
		return nil, true
	}
	x.lastSent[gpu] = now
	return classify.Classify(classify.NormalizedSignals{XIDs: []uint{uint(ev.data.EventData)}}), true
}

// register creates the event set and registers every GPU for critical XID
// events. It reports false until NVML is initialized and the set exists.
func (x *XIDs) register() bool {
	lib, _, ok := x.reader.Library()
	if !ok {
		return false
	}
	type registered struct {
		set     gonvml.EventSet
		devices map[gonvml.Device]int
	}
	r, status := run(x.check, func() registered {
		set, devices := registerXIDEvents(lib)
		return registered{set, devices}
	})
	if status == Stuck || r.set == nil {
		return false
	}
	x.set, x.devices = r.set, r.devices
	return true
}

// registerXIDEvents creates an event set and registers each GPU that supports
// critical XID events. A nil set means registration failed and should be
// tried again.
func registerXIDEvents(lib gonvml.Interface) (gonvml.EventSet, map[gonvml.Device]int) {
	set, ret := lib.EventSetCreate()
	if outcome("nvmlEventSetCreate", ret) != Observed {
		return nil, nil
	}
	count, ret := lib.DeviceGetCount()
	if outcome("nvmlDeviceGetCount", ret) != Observed {
		set.Free()
		return nil, nil
	}
	devices := map[gonvml.Device]int{}
	for i := range count {
		dev, ret := lib.DeviceGetHandleByIndex(i)
		if outcome("nvmlDeviceGetHandleByIndex", ret) != Observed {
			continue
		}
		if outcome("nvmlDeviceRegisterEvents", dev.RegisterEvents(gonvml.EventTypeXidCriticalError, set)) == Observed {
			devices[dev] = i
		}
	}
	return set, devices
}

func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
