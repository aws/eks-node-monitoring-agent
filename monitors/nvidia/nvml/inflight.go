package nvml

import (
	"sync"
	"time"
)

// DefaultTimeout bounds one NVML call.
const DefaultTimeout = 10 * time.Second

// stuckThreshold is how many consecutive runs every check must be stuck
// before the reader reports the driver as hung.
const stuckThreshold = 5

// Check runs one NVML call at a time under a deadline. The deadline returns
// control to the caller but cannot cancel a cgo call: the goroutine and its OS
// thread stay in the driver until it answers. While a call is outstanding, the
// check skips its next run and reports Stuck, so a hung driver holds at most
// one thread per check.
type Check struct {
	name    string
	timeout time.Duration

	mu          sync.Mutex
	pending     <-chan struct{} // closed when the outstanding call returns; nil when there is none
	stuckCycles int
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
func (c *Check) StuckCycles() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stuckCycles
}

// run calls fn unless an earlier call of c is still outstanding, and waits up
// to c's timeout for it. On Done it returns fn's value. On Stuck it returns
// the zero value; fn finishes in the background and its result is dropped.
func run[T any](c *Check, fn func() T) (T, Status) {
	var zero T

	c.mu.Lock()
	if c.pending != nil {
		select {
		case <-c.pending:
			// The earlier call returned after its deadline. The driver
			// answered, so this run makes a new call.
			c.pending = nil
		default:
			c.stuckCycles++
			c.mu.Unlock()
			return zero, Stuck
		}
	}
	done := make(chan struct{})
	result := make(chan T, 1)
	c.pending = done
	c.mu.Unlock()

	go func() {
		defer close(done)
		result <- fn()
	}()

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case v := <-result:
		c.mu.Lock()
		c.pending = nil
		c.stuckCycles = 0
		c.mu.Unlock()
		return v, Done
	case <-timer.C:
		c.mu.Lock()
		c.stuckCycles++
		c.mu.Unlock()
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
