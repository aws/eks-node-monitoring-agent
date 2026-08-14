package collect_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

func TestLogCollectorAccessor(t *testing.T) {
	accessor, err := collect.NewAccessor(context.Background(), collect.Config{
		Root:        "/",
		Destination: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []collect.Collector{
		&collect.Instance{},
		&collect.Region{},
		&collect.CommonLogs{},
		&collect.Containerd{},
		&collect.CNI{},
		&collect.Kernel{},
		&collect.Disk{},
		&collect.SELinux{},
		&collect.Nvidia{},
		&collect.IPTables{},
		&collect.IPAMD{},
		&collect.System{},
		&collect.Nodeadm{},
		&collect.Throttles{},
		&collect.Pressure{},
		&collect.Sandbox{},
		&collect.Kubernetes{},
		&collect.Networking{},
	} {
		// these are not expected to succeed on the build host, but running
		// them for unit test coverage because they are non-destructive.
		c.Collect(accessor)
	}
}

// Regression test for issue #219: a collector command that blocks must fail with
// a timeout so that the existing per collector error handling can report it,
// instead of hanging the whole capture with no error to report.
func TestAccessorCommandTimeout(t *testing.T) {
	acc, err := collect.NewAccessor(context.Background(), collect.Config{
		Root:           "/",
		Destination:    t.TempDir(),
		CommandTimeout: 100 * time.Millisecond,
	})
	require.NoError(t, err)

	t.Run("CombinedOutput", func(t *testing.T) {
		start := time.Now()
		_, err := acc.CombinedOutput("sleep", "30")

		require.ErrorIs(t, err, osext.ErrTimeout)
		assert.Less(t, time.Since(start), 5*time.Second, "must return at the deadline")
	})

	t.Run("Output", func(t *testing.T) {
		_, err := acc.Output("sleep", "30")
		require.ErrorIs(t, err, osext.ErrTimeout)
	})

	// CommandOutput is what the collectors use, and its error is what ends up in
	// log-capture-errors.log and in the failed task count.
	t.Run("CommandOutput", func(t *testing.T) {
		err := acc.CommandOutput([]string{"sleep", "30"}, "slow.txt", collect.CommandOptionsNone)
		require.Error(t, err)
		// the message is what lands in log-capture-errors.log, and the wrapped
		// error is what lets a caller tell a timeout from a failed command.
		assert.Contains(t, err.Error(), "executing command \"sleep 30\"")
		assert.Contains(t, err.Error(), "timed out")
		assert.ErrorIs(t, err, osext.ErrTimeout)
	})

	t.Run("IgnoredFailureStillReturnsNil", func(t *testing.T) {
		err := acc.CommandOutput([]string{"sleep", "30"}, "slow.txt", collect.CommandOptionsIgnoreFailure)
		assert.NoError(t, err)
	})
}

func TestAccessorCommandTimeoutDefault(t *testing.T) {
	dst := t.TempDir()
	acc, err := collect.NewAccessor(context.Background(), collect.Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	// a command well within the default bound still succeeds.
	require.NoError(t, acc.CommandOutput([]string{"echo", "hello"}, "echo.txt", collect.CommandOptionsNone))
	assert.FileExists(t, filepath.Join(dst, "echo.txt"))
}

// Cancelling the collection context stops in-flight commands, which is what lets
// the controller give up on a capture.
func TestAccessorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	acc, err := collect.NewAccessor(ctx, collect.Config{Root: "/", Destination: t.TempDir()})
	require.NoError(t, err)
	cancel()

	_, err = acc.CombinedOutput("sleep", "30")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAccessorCopyFileCancelled(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.log")
	require.NoError(t, os.WriteFile(src, []byte("contents"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	dest := t.TempDir()
	acc, err := collect.NewAccessor(ctx, collect.Config{Root: "/", Destination: dest})
	require.NoError(t, err)
	cancel()

	err = acc.CopyFile(src, "copied.log")
	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, filepath.Join(dest, "copied.log"),
		"a cancelled copy must not leave a destination file behind")
}

func TestAccessorCopyDirCancelled(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a.log"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "b.log"), []byte("b"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	acc, err := collect.NewAccessor(ctx, collect.Config{Root: "/", Destination: t.TempDir()})
	require.NoError(t, err)
	cancel()

	err = acc.CopyDir(srcDir, "copied")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAccessorCopyFileSucceedsWhenLive(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.log")
	require.NoError(t, os.WriteFile(src, []byte("contents"), 0o644))

	dest := t.TempDir()
	acc, err := collect.NewAccessor(context.Background(), collect.Config{Root: "/", Destination: dest})
	require.NoError(t, err)

	require.NoError(t, acc.CopyFile(src, "copied.log"))
	body, err := os.ReadFile(filepath.Join(dest, "copied.log"))
	require.NoError(t, err)
	assert.Equal(t, "contents", string(body), "bounding the copy must not change what it copies")
}

func TestAccessorCopyDirSucceedsWhenLive(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a.log"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "nested", "b.log"), []byte("b"), 0o644))

	dest := t.TempDir()
	acc, err := collect.NewAccessor(context.Background(), collect.Config{Root: "/", Destination: dest})
	require.NoError(t, err)

	require.NoError(t, acc.CopyDir(srcDir, "copied"))
	for path, want := range map[string]string{
		filepath.Join(dest, "copied", "a.log"):           "a",
		filepath.Join(dest, "copied", "nested", "b.log"): "b",
	} {
		body, err := os.ReadFile(path)
		require.NoErrorf(t, err, "expected %s to be copied", path)
		assert.Equal(t, want, string(body))
	}
}

// A copy cancelled mid-stream stops at a read boundary rather than running to the
// end of a large source. Cancellation is driven from the source itself so the
// assertions always run: a time based cancel would let a fast machine finish the
// copy first and pass without testing anything.
func TestAccessorCopyFileCancelledMidStream(t *testing.T) {
	const size = 4 << 20 // several io.Copy chunks
	src := filepath.Join(t.TempDir(), "big.log")
	require.NoError(t, os.WriteFile(src, make([]byte, size), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dest := t.TempDir()
	acc, err := collect.NewAccessor(ctx, collect.Config{Root: "/", Destination: dest})
	require.NoError(t, err)

	// cancel once the copy is demonstrably under way, using the destination file
	// as the signal so this cannot race the copy to completion.
	go func() {
		for {
			if info, err := os.Stat(filepath.Join(dest, "big.log")); err == nil && info.Size() > 0 {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	err = acc.CopyFile(src, "big.log")
	require.ErrorIs(t, err, context.Canceled, "a cancelled copy must report cancellation")

	info, statErr := os.Stat(filepath.Join(dest, "big.log"))
	require.NoError(t, statErr)
	assert.Less(t, info.Size(), int64(size),
		"a cancelled copy must stop before writing the whole source")
}
