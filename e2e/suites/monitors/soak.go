package monitors

import (
	"context"
	"flag"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

var (
	soakDuration time.Duration
)

func init() {
	flag.DurationVar(&soakDuration, "soak-duration", 12*time.Minute, "Duration to soak while monitoring the agent for unexpected errors")
}

// Soak checks that the period monitor tasks do not cause bugs or panics in the
// monitoring agent at runtime.
func Soak(healthCheck func(context.Context) error) types.Feature {
	return features.New("Soak").
		WithLabel("type", "soak").
		Assess("Watch", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var waiter <-chan time.Time
			if soakDuration > 0 {
				t.Logf("watching agent health for %.2f minutes...", soakDuration.Minutes())
				waiter = time.After(soakDuration)
			} else {
				t.Log("watching agent health indefinitely...")
			}
			watcher := time.NewTicker(time.Minute)
			defer watcher.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx
				case <-watcher.C:
					if err := healthCheck(ctx); err != nil {
						t.Fatalf("failed health check: %s", err)
					}
				case <-waiter:
					return ctx
				}
			}
		}).
		Feature()
}
