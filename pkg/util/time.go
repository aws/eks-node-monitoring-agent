package util

import (
	"math/rand"
	"time"
)

func TimeToJournalTimestamp(t time.Time) uint64 {
	return uint64(t.UnixNano() / 1000)
}

func TimeTickWithJitter(baseDuration time.Duration) <-chan time.Time {
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
		time.Sleep(delay)
		ticker.Reset(baseDuration)
	}()

	return ticker.C
}
