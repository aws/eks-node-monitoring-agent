package scrape

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func counter(v float64) map[string][]Series {
	return map[string][]Series{"foo_total": {{Labels: map[string]string{"api": "A"}, Value: v}}}
}

func TestBaselines(t *testing.T) {
	b := NewBaselines()
	t0, t1, t2, t3 := time.Unix(0, 0), time.Unix(300, 0), time.Unix(600, 0), time.Unix(900, 0)

	assert.Empty(t, b.Observe(t0, counter(10)), "no delta on first sight")

	d := b.Observe(t1, counter(15))
	assert.Equal(t, 5.0, d[0].Value)
	d = b.Observe(t2, counter(18))
	assert.Equal(t, 8.0, d[0].Value)
	assert.Equal(t, t0, d[0].Start)

	b.Advance()
	d = b.Observe(t3, counter(20))
	assert.Equal(t, 2.0, d[0].Value)
	assert.Equal(t, t2, d[0].Start, "the baseline starts at the scrape it came from")

	b.Advance()
	assert.Empty(t, b.Observe(time.Unix(1200, 0), counter(1)), "a decrease resets the series at its current value")
	d = b.Observe(time.Unix(1500, 0), counter(3))
	assert.Equal(t, 2.0, d[0].Value)
	assert.Equal(t, time.Unix(1200, 0), d[0].Start)
}

func TestBaselines_ResetAfterHeld(t *testing.T) {
	b := NewBaselines()
	t0, t1, t2, t3 := time.Unix(0, 0), time.Unix(300, 0), time.Unix(600, 0), time.Unix(900, 0)
	b.Observe(t0, counter(10))
	b.Observe(t1, counter(100))

	d := b.Observe(t2, counter(20))
	assert.Equal(t, 90.0, d[0].Value, "the held increase before the reset is carried over, and the reset adds nothing")
	assert.Equal(t, t0, d[0].Start)
	d = b.Observe(t3, counter(25))
	assert.Equal(t, 95.0, d[0].Value, "the carry stays until it's advanced")

	b.Advance()
	d = b.Observe(time.Unix(1200, 0), counter(30))
	assert.Equal(t, 5.0, d[0].Value)
	assert.Equal(t, t3, d[0].Start)
}

func TestBaselines_MissingAfterHeld(t *testing.T) {
	b := NewBaselines()
	b.Observe(time.Unix(0, 0), counter(10))
	b.Observe(time.Unix(300, 0), counter(15))
	b.Observe(time.Unix(600, 0), nil)
	b.Advance()
	d := b.Observe(time.Unix(900, 0), counter(16))
	assert.Equal(t, 6.0, d[0].Value, "the held increase isn't advanced past while the series is missing")
	assert.Equal(t, time.Unix(0, 0), d[0].Start)
}

func TestBaselines_Eviction(t *testing.T) {
	b := NewBaselines()
	b.Observe(time.Unix(0, 0), counter(10))
	for i := 0; i < evictAfter; i++ {
		b.Observe(time.Unix(0, 0), nil)
	}
	assert.Empty(t, b.series, "missing for evictAfter scrapes")

	b.Observe(time.Unix(0, 0), counter(10))
	for i := 0; i < evictAfter; i++ {
		b.ScrapeFailed()
	}
	assert.Empty(t, b.series, "evictAfter failed scrapes in a row")
}
