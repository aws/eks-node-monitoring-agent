package nvml

import (
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds how long one run of a check waits in the NVIDIA
// driver. A healthy driver answers a call in milliseconds, so a run still in
// the driver after 10 seconds is stuck, not slow, even one that reads every
// GPU. It is also no longer than the shortest interval any check polls at, so
// a check stops waiting before its own next run.
const DefaultTimeout = 10 * time.Second

// stallDuration is how long every check that reads the GPUs must have had a
// call in the driver before the reader reports the driver as not responding.
// A healthy driver answers a call in milliseconds, so a call still in the
// driver after two minutes, twelve times DefaultTimeout, is not a slow call.
// One stuck check can be a single slow call; every check stuck means the
// driver, or a GPU every read waits on, has stopped answering. It is a
// duration rather than a run count, so a check that polls every 5 minutes
// doesn't take five polls to count as stalled.
const stallDuration = 2 * time.Minute

// Check runs one NVML call at a time under a deadline. The deadline returns
// control to the caller but cannot cancel a cgo call: the goroutine and its OS
// thread stay in the driver until it answers. While a call is outstanding, the
// check skips its next run and reports Stuck, so a hung driver holds at most
// one thread per check.
//
// A Check must be run from one goroutine at a time.
type Check struct {
	name    string
	timeout time.Duration
	now     func() time.Time

	// pending is only touched by the goroutine that calls run. It is closed
	// when the outstanding call returns, and nil when there is none.
	pending <-chan struct{}
	// inDriverSince is when the call now in the driver started, in Unix
	// nanoseconds, and zero when no call is in the driver. run sets it when
	// it starts a call. The call's own goroutine clears it as soon as the
	// call returns, so a check whose driver answered doesn't look stalled
	// until its next run. Reconcile reads it.
	inDriverSince atomic.Int64
}

func newCheck(name string, timeout time.Duration, now func() time.Time) *Check {
	return &Check{name: name, timeout: timeout, now: now}
}

// Status is the result of one run of a Check.
type Status int

const (
	// Done means the call returned within the deadline.
	Done Status = iota
	// Stuck means a call is still in the driver, from this run or an earlier one.
	Stuck
)

// run calls fn unless an earlier call of c is still outstanding, and waits up
// to c's timeout for it. On Done it returns fn's value. On Stuck it returns
// the zero value; fn finishes in the background and its result is dropped.
func run[T any](c *Check, fn func() T) (T, Status) {
	var zero T
	if c.pending != nil {
		select {
		case <-c.pending:
			// The earlier call returned after its deadline. The driver
			// answered, so this run makes a new call.
			c.pending = nil
		default:
			return zero, Stuck
		}
	}

	done := make(chan struct{})
	result := make(chan T, 1)
	c.pending = done
	c.inDriverSince.Store(c.now().UnixNano())
	go func() {
		defer close(done)
		v := fn()
		c.inDriverSince.Store(0)
		result <- v
	}()

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case v := <-result:
		c.pending = nil
		return v, Done
	case <-timer.C:
		return zero, Stuck
	}
}

// stalledFor reports whether there is at least one check and every check has
// had a call in the driver for at least d at now.
func stalledFor(checks []*Check, now time.Time, d time.Duration) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		since := c.inDriverSince.Load()
		if since == 0 || now.Sub(time.Unix(0, since)) < d {
			return false
		}
	}
	return true
}
