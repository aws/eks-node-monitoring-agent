// Package captureguard limits a resource to one capture at a time, shared
// across capture types that must not run concurrently.
package captureguard

import (
	"sync/atomic"
	"time"
)

// Guard allows only one caller at a time to hold it. The zero value is ready
// to use.
//
// Release must be called only by whoever actually won TryAcquire, and only
// after the guarded work has genuinely finished, even if the caller that was
// waiting on that work gave up first (for example on a timeout) and is not
// the one calling Release. Releasing early lets a second caller start while
// the first is still running, which is exactly what the guard exists to
// prevent.
//
// A Guard is safe for concurrent use.
type Guard struct {
	running   atomic.Bool
	startedAt atomic.Int64
}

// TryAcquire reports whether this call claimed the guard. Only the winner
// should proceed; it must call Release when done.
func (g *Guard) TryAcquire() bool {
	if !g.running.CompareAndSwap(false, true) {
		return false
	}
	// stored only by the winner: if a refused caller stored it too, a
	// concurrent read could see a stale or zero value and compute the held
	// duration as zero.
	g.startedAt.Store(time.Now().UnixNano())
	return true
}

// Release frees the guard for the next TryAcquire.
func (g *Guard) Release() {
	// zeroed before running goes false: if the order were reversed, a
	// TryAcquire winner could race in between the two stores, store its own
	// fresh startedAt, and then have this call's zero-store clobber it. With
	// startedAt cleared first, the only store that can make it non-zero again
	// is the next winner's, so HeldFor never reports a stale value left over
	// from the previous holder.
	g.startedAt.Store(0)
	g.running.Store(false)
}

// HeldFor reports how long the guard has been held, and false if the winner
// has not yet stored its start time (the brief window right after TryAcquire
// succeeds, or if the guard has never been acquired).
func (g *Guard) HeldFor() (time.Duration, bool) {
	startedAt := g.startedAt.Load()
	if startedAt == 0 {
		return 0, false
	}
	return time.Since(time.Unix(0, startedAt)), true
}
