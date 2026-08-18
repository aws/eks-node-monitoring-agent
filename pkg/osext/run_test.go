package osext_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

func TestOutput(t *testing.T) {
	t.Run("ReturnsStdout", func(t *testing.T) {
		out, err := osext.NewExec("/").Output(context.Background(), time.Minute, "echo", "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello\n", string(out))
	})

	t.Run("ReturnsCommandError", func(t *testing.T) {
		_, err := osext.NewExec("/").Output(context.Background(), time.Minute, "false")
		require.Error(t, err)
		// a command that ran and failed is not a timeout.
		assert.NotErrorIs(t, err, osext.ErrTimeout)
	})

	// a command that outlives its bound must release the caller, and must do so
	// at the deadline rather than when the command eventually finishes.
	t.Run("TimeoutReleasesCaller", func(t *testing.T) {
		start := time.Now()
		_, err := osext.NewExec("/").Output(context.Background(), 100*time.Millisecond, "sleep", "30")
		elapsed := time.Since(start)

		require.ErrorIs(t, err, osext.ErrTimeout)
		assert.Contains(t, err.Error(), "sleep")
		assert.Less(t, elapsed, 5*time.Second, "must return at the deadline, not on command completion")
	})

	// a cancelled parent is reported as cancellation, not as a command timeout,
	// so that shutdown is distinguishable from a wedged collector.
	t.Run("ParentCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := osext.NewExec("/").Output(ctx, time.Minute, "sleep", "30")
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, osext.ErrTimeout)
	})
}

func TestCombinedOutput(t *testing.T) {
	t.Run("IncludesStderr", func(t *testing.T) {
		out, err := osext.NewExec("/").CombinedOutput(context.Background(), time.Minute, "sh", "-c", "echo out; echo err >&2")
		require.NoError(t, err)
		assert.Contains(t, string(out), "out")
		assert.Contains(t, string(out), "err")
	})

	t.Run("TimeoutReleasesCaller", func(t *testing.T) {
		start := time.Now()
		_, err := osext.NewExec("/").CombinedOutput(context.Background(), 100*time.Millisecond, "sleep", "30")

		require.ErrorIs(t, err, osext.ErrTimeout)
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}

// the package level helpers give the caller control of the whole command, which
// collectors need in order to set a custom environment.
func TestCommandFuncHelpers(t *testing.T) {
	t.Run("Output", func(t *testing.T) {
		out, err := osext.Output(context.Background(), time.Minute, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "sh", "-c", "echo $NMA_TEST_VAR")
			cmd.Env = append(cmd.Environ(), "NMA_TEST_VAR=set")
			return cmd
		})
		require.NoError(t, err)
		assert.Equal(t, "set\n", string(out))
	})

	t.Run("Timeout", func(t *testing.T) {
		_, err := osext.CombinedOutput(context.Background(), 100*time.Millisecond, func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "30")
		})
		require.ErrorIs(t, err, osext.ErrTimeout)
	})
}
