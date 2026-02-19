package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_ContextStopsLogger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Nanosecond)
	defer cancel()
	var buffer bytes.Buffer
	loggerOpts := Settings{
		LogInterval: 1 * time.Nanosecond,
	}
	if err := NewDiagnosticLogger(&buffer, loggerOpts).Start(ctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("returned error: %s", err)
		}
	}
	if buffer.Len() == 0 {
		t.Errorf("should have written diagnostic to logger")
	}
}

func TestUtils(t *testing.T) {
	t.Run("TailX", func(t *testing.T) {
		assert.Equal(t, "World", string(tailx([]byte("Hello, World"), 5)))
		assert.Equal(t, "Hello, World", string(tailx([]byte("Hello, World"), 99)))
		assert.Equal(t, "World", string(tailx([]byte("Hello,\nWorld"), 5)))
		assert.Equal(t, "World", string(tailx([]byte("Hello,\nWorld"), 7)))
		assert.Equal(t, "World\nAgain!", string(tailx([]byte("Hello,\nWorld\nAgain!"), 15)))
		assert.Equal(t, "Hello,\nWorld\nAgain!", string(tailx([]byte("Hello,\nWorld\nAgain!"), 99)))
	})
}
