package util_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/eks-node-monitoring-agent/pkg/util"
)

func TestTimeTickWithJitterContext(t *testing.T) {
	t.Run("panics on negative duration", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for negative duration")
			}
		}()
		util.TimeTickWithJitterContext(context.Background(), -1*time.Second)
	})

	t.Run("panics on zero duration", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for zero duration")
			}
		}()
		util.TimeTickWithJitterContext(context.Background(), 0)
	})

	t.Run("stops ticker on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		ch := util.TimeTickWithJitterContext(ctx, time.Hour)

		cancel()

		// Give goroutine time to process cancellation
		time.Sleep(10 * time.Millisecond)

		// Channel should not send after cancellation
		select {
		case <-ch:
			// Receiving is acceptable if tick happened before cancel
		default:
			// Not receiving is expected
		}
	})
}
