package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeStub creates an executable shell script named name in bin.
func writeStub(t *testing.T, bin, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
}

func TestSociSnapshotter(t *testing.T) {
	t.Run("unit active collects status and journal", func(t *testing.T) {
		bin := t.TempDir()
		writeStub(t, bin, "systemctl", `case "$1" in
  is-active) exit 0 ;;
  status) echo "soci-snapshotter active (running)" ;;
esac`)
		writeStub(t, bin, "journalctl", `echo "soci journal"`)
		t.Setenv("PATH", bin)

		dst := t.TempDir()
		// Root must be "/" here: a non-root Root makes osext chroot the
		// command, which an unprivileged test cannot do. The config.toml copy
		// is therefore not covered: with Root "/" it reads the real
		// /etc/soci-snapshotter-grpc/config.toml of the machine running the test.
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, sociSnapshotter(acc))

		status, err := os.ReadFile(filepath.Join(dst, "containerd/soci-snapshotter/soci-snapshotter-status.txt"))
		require.NoError(t, err)
		assert.Equal(t, "soci-snapshotter active (running)\n", string(status))

		journal, err := os.ReadFile(filepath.Join(dst, "containerd/soci-snapshotter/soci-snapshotter-log.txt"))
		require.NoError(t, err)
		assert.Equal(t, "soci journal\n", string(journal))
	})

	t.Run("unit failed collects status and journal", func(t *testing.T) {
		// A failed unit is not active, so is-active exits 3, but is-failed
		// exits 0. The journal and status are what explain the failure.
		bin := t.TempDir()
		writeStub(t, bin, "systemctl", `case "$1" in
  is-active) exit 3 ;;
  is-failed) exit 0 ;;
  status) echo "soci-snapshotter failed (Result: exit-code)" ;;
esac`)
		writeStub(t, bin, "journalctl", `echo "failed to open config file"`)
		t.Setenv("PATH", bin)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, sociSnapshotter(acc))

		status, err := os.ReadFile(filepath.Join(dst, "containerd/soci-snapshotter/soci-snapshotter-status.txt"))
		require.NoError(t, err)
		assert.Equal(t, "soci-snapshotter failed (Result: exit-code)\n", string(status))

		journal, err := os.ReadFile(filepath.Join(dst, "containerd/soci-snapshotter/soci-snapshotter-log.txt"))
		require.NoError(t, err)
		assert.Equal(t, "failed to open config file\n", string(journal))
	})

	t.Run("unit neither active nor failed skips without error", func(t *testing.T) {
		// An absent or never-started unit: both is-active and is-failed exit
		// non-zero.
		bin := t.TempDir()
		writeStub(t, bin, "systemctl", `exit 3`)
		t.Setenv("PATH", bin)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, sociSnapshotter(acc))
		_, err = os.Stat(filepath.Join(dst, "containerd/soci-snapshotter"))
		assert.True(t, os.IsNotExist(err), "no output expected when the unit is neither active nor failed")
	})
}
