package captureguard

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryAcquire_SecondCallerFailsWhileHeld(t *testing.T) {
	var g Guard

	require.True(t, g.TryAcquire())
	assert.False(t, g.TryAcquire())
}

func TestTryAcquire_SucceedsAfterRelease(t *testing.T) {
	var g Guard

	require.True(t, g.TryAcquire())
	g.Release()
	assert.True(t, g.TryAcquire())
}

func TestHeldFor_FalseBeforeStartedAtStored(t *testing.T) {
	var g Guard

	_, ok := g.HeldFor()
	assert.False(t, ok, "never acquired: no duration to report")
}

func TestHeldFor_TrueWithSensibleDurationAfterAcquire(t *testing.T) {
	var g Guard

	require.True(t, g.TryAcquire())
	const sleep = 10 * time.Millisecond
	time.Sleep(sleep)

	held, ok := g.HeldFor()
	require.True(t, ok)
	assert.GreaterOrEqual(t, held, sleep, "should be at least as long as the real sleep")
	assert.Less(t, held, time.Minute, "sanity bound: should not be absurdly large")
}

func TestHeldFor_FalseAfterRelease(t *testing.T) {
	var g Guard

	require.True(t, g.TryAcquire())
	g.Release()

	_, ok := g.HeldFor()
	assert.False(t, ok, "startedAt must be zeroed on release, not just running")
}

func TestTryAcquire_OnlyOneWinnerAmongConcurrentCallers(t *testing.T) {
	const n = 30
	var g Guard
	var wins atomic.Int32

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			if g.TryAcquire() {
				wins.Add(1)
			}
		}()
	}
	start.Done()
	wg.Wait()

	assert.Equal(t, int32(1), wins.Load(), "exactly one caller must win the guard")
}
