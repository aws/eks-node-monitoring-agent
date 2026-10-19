package nvml

import (
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds how long a check waits for one NVML call. A healthy
// driver answers in milliseconds, so a call still running after 10 seconds is
// stuck, not slow. It is also no longer than the shortest interval any check
// polls at, so a check stops waiting before its own next run.
const DefaultTimeout = 10 * time.Second

// stuckThreshold is how many consecutive runs every check must be stuck
// before the reader reports the driver as hung. One stuck run can be a short
// stall, such as a GPU reset or heavy load holding the driver lock, and one
// stuck check can be a single slow call. Every check stuck for five runs in a
// row means the driver as a whole has stopped answering. Because every check
// has to be stuck, the slowest check's interval sets how long that takes.
const stuckThreshold = 5

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

	// pending is only touched by the goroutine that calls run. It is closed
	// when the outstanding call returns, and nil when there is none.
	pending <-chan struct{}
	// stuck is written by run and read by Reconcile through StuckCycles.
	stuck atomic.Int32
}

// Status is the result of one run of a Check.
type Status int

const (
	// Done means the call returned within the deadline.
	Done Status = iota
	// Stuck means a call is still in the driver, from this run or an earlier one.
	Stuck
)

// StuckCycles returns how many runs in a row have ended Stuck.
func (c *Check) StuckCycles() int { return int(c.stuck.Load()) }

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
			c.stuck.Add(1)
			return zero, Stuck
		}
	}

	done := make(chan struct{})
	result := make(chan T, 1)
	c.pending = done
	go func() {
		defer close(done)
		result <- fn()
	}()

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case v := <-result:
		c.pending = nil
		c.stuck.Store(0)
		return v, Done
	case <-timer.C:
		c.stuck.Add(1)
		return zero, Stuck
	}
}

// allStuck reports whether there is at least one check and every check has
// been stuck for stuckThreshold consecutive runs.
func allStuck(checks []*Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if c.StuckCycles() < stuckThreshold {
			return false
		}
	}
	return true
}
