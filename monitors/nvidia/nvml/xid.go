package nvml

import (
	"context"
	"slices"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

// xidRetryInterval is how often Wait retries GPUs that failed to register for
// XID events. NVML does not record events from before a GPU registers, so a
// GPU's XIDs are missed until a retry succeeds.
const xidRetryInterval = 30 * time.Second

// XIDs receives critical XID events from every GPU through one NVML event
// set, the same events DCGM's XID policy reads.
type XIDs struct {
	reader *Reader
	check  *Check
	now    func() time.Time
	sleep  func(context.Context, time.Duration)

	// Only the goroutine that calls Wait touches the fields below. missing
	// holds the indices of GPUs that failed to register, retried no earlier
	// than retryAt. lastSent is when each device last reported, for the
	// repeat floor; events from a device NMA did not register still get a
	// floor of their own.
	set      gonvml.EventSet
	missing  []int
	retryAt  time.Time
	lastSent map[gonvml.Device]time.Time
}

// NewXIDs returns the XID reader for r.
func NewXIDs(r *Reader) *XIDs {
	return &XIDs{
		reader: r,
		// Not registered with the reader: the wait is on the event set, not
		// on one GPU, so it can keep returning while one GPU is wedged, and
		// counting it would hide that stall.
		check:    newCheck("xid", DefaultTimeout, r.now),
		now:      time.Now,
		sleep:    sleepContext,
		lastSent: map[gonvml.Device]time.Time{},
	}
}

// Wait waits for the next XID event and returns its condition, or nothing if
// none arrives within about five seconds. ok is false when it could not wait
// in the driver: NVML is not initialized, registration failed, a call is
// stuck, or the wait failed. Wait pauses before returning in those cases, so
// a caller can loop on it without spinning. A false ok is unknown, not a
// quiet period. Call it from one goroutine.
func (x *XIDs) Wait(ctx context.Context) (conds []monitor.Condition, ok bool) {
	if !x.register(ctx) {
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
	now := x.now()
	if last, seen := x.lastSent[ev.data.Device]; seen && now.Sub(last) < policyRepeatFloor {
		return nil, true
	}
	x.lastSent[ev.data.Device] = now
	return classify.Classify(classify.NormalizedSignals{XIDs: []uint{uint(ev.data.EventData)}}), true
}

// register creates the event set on first use, registers every GPU for
// critical XID events, and retries GPUs that failed at most every
// xidRetryInterval. It reports false until the set exists.
func (x *XIDs) register(ctx context.Context) bool {
	if x.set != nil && (len(x.missing) == 0 || x.now().Before(x.retryAt)) {
		return true
	}
	lib, _, ok := x.reader.Library()
	if !ok {
		return false
	}
	set, retry := x.set, x.missing
	r, status := run(x.check, func() registration { return registerXIDEvents(lib, set, retry) })
	if status == Stuck || r.set == nil {
		return x.set != nil
	}
	if !slices.Equal(r.missing, x.missing) && len(r.missing) > 0 {
		log.FromContext(ctx).Info("GPUs failed to register for XID events and will be retried; their XIDs are missed until then",
			"gpus", r.missing, "retryInterval", xidRetryInterval)
	}
	x.set, x.missing, x.retryAt = r.set, r.missing, x.now().Add(xidRetryInterval)
	xidUnregisteredGPUs.Set(float64(len(x.missing)))
	return true
}

// registration is an event set and the GPUs still to register in it.
type registration struct {
	set     gonvml.EventSet
	missing []int
}

// registerXIDEvents registers GPUs in set for critical XID events: every GPU
// when set is nil, which first creates the set, otherwise only the GPUs in
// retry. A GPU whose handle or registration fails goes back on the missing
// list. NOT_SUPPORTED means the GPU has no critical XID events, so it is not
// retried. A nil set means the set could not be created or the GPUs could not
// be counted.
func registerXIDEvents(lib gonvml.Interface, set gonvml.EventSet, retry []int) registration {
	if set == nil {
		created, ret := lib.EventSetCreate()
		if outcome("nvmlEventSetCreate", ret) != Observed {
			return registration{}
		}
		count, ret := lib.DeviceGetCount()
		if outcome("nvmlDeviceGetCount", ret) != Observed {
			created.Free()
			return registration{}
		}
		set, retry = created, make([]int, count)
		for i := range retry {
			retry[i] = i
		}
	}
	var missing []int
	for _, i := range retry {
		dev, ret := lib.DeviceGetHandleByIndex(i)
		if outcome("nvmlDeviceGetHandleByIndex", ret) != Observed {
			missing = append(missing, i)
			continue
		}
		ret = dev.RegisterEvents(gonvml.EventTypeXidCriticalError, set)
		if ret != gonvml.ERROR_NOT_SUPPORTED && outcome("nvmlDeviceRegisterEvents", ret) != Observed {
			missing = append(missing, i)
		}
	}
	return registration{set, missing}
}

func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
