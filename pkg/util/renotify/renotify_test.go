package renotify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTracker(t *testing.T) {
	const interval = 30 * time.Minute
	start := time.Unix(0, 0)
	tr := New[string](interval)

	// A first state is notified.
	assert.True(t, tr.ShouldNotify("a", start))

	// Not recorded until Notified: a failed send is retried.
	assert.True(t, tr.ShouldNotify("a", start.Add(time.Minute)))
	tr.Notified("a", start.Add(time.Minute))

	// Unchanged: not notified again until interval has passed.
	assert.False(t, tr.ShouldNotify("a", start.Add(interval)))
	assert.True(t, tr.ShouldNotify("a", start.Add(time.Minute+interval)))

	// A different state is notified right away.
	assert.True(t, tr.ShouldNotify("b", start.Add(2*time.Minute)))

	// After Reset (healthy again), the same state is notified right away.
	tr.Reset()
	assert.True(t, tr.ShouldNotify("a", start.Add(2*time.Minute)))
}
