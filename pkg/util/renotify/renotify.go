// Package renotify decides when to send a notification again for a state that
// has not changed.
package renotify

import "time"

// Tracker decides whether to notify for a state: when it differs from the last
// state notified, or when Interval has passed since that notification. An
// unchanged state is otherwise observed, and would be notified, on every check.
// Every NMA Warning on a node shares one client-go spam-filter bucket, so
// repeats crowd out other events; Interval keeps an unchanged state's Event
// from aging out (for example under the apiserver's default 1h event TTL).
//
// A Tracker is not safe for concurrent use.
type Tracker[K comparable] struct {
	interval     time.Duration
	lastKey      K
	lastNotified time.Time
}

// New returns a Tracker that notifies an unchanged state again after interval.
func New[K comparable](interval time.Duration) *Tracker[K] {
	return &Tracker[K]{interval: interval}
}

// ShouldNotify reports whether to notify for key at now. It does not record
// anything: call Notified once the notification has been sent, so a failed
// send is retried on the next check.
func (t *Tracker[K]) ShouldNotify(key K, now time.Time) bool {
	return key != t.lastKey || now.Sub(t.lastNotified) >= t.interval
}

// Notified records that key was notified at now.
func (t *Tracker[K]) Notified(key K, now time.Time) {
	t.lastKey, t.lastNotified = key, now
}

// Reset forgets the last notification, so the next state is notified right
// away. Call it when the state is healthy again.
func (t *Tracker[K]) Reset() {
	var zero K
	t.lastKey, t.lastNotified = zero, time.Time{}
}
