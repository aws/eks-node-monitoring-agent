package util

import (
	"context"
	"math/rand"
	"time"
)

func TimeToJournalTimestamp(t time.Time) uint64 {
	return uint64(t.UnixNano() / 1000)
}

// TimeTickWithJitterContext returns a channel that fires at baseDuration intervals
// with an initial jitter delay. The ticker is automatically stopped when ctx is cancelled.
func TimeTickWithJitterContext(ctx context.Context, baseDuration time.Duration) <-chan time.Time {
	if baseDuration <= 0 {
		panic("baseDuration must be positive")
	}

	// Fixed jitter percentage is 20% (0.2)
	jitter := 0.2
	// Initialize the ticker with a long duration. Then, before
	// it sends the first tick reset the ticker with some delay.
	// This ensures that the jitter is only at the start of the timer
	ticker := time.NewTicker(24 * time.Hour)

	go func() {
		maxDelay := float64(baseDuration) * jitter
		delay := time.Duration(rand.Float64() * maxDelay)

		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-time.After(delay):
			ticker.Reset(baseDuration)
		}

		<-ctx.Done()
		ticker.Stop()
	}()

	return ticker.C
}
