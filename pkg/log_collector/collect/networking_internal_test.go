package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetworkdUnitFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"etc/systemd/network/10-eth0.network.d", // drop-in directory: read by systemd-analyze, not listed
		"run/systemd/network",
		"usr/lib/systemd/network",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	for _, f := range []string{
		"etc/systemd/network/10-eth0.network",
		"run/systemd/network/70-eth1.network",
		"usr/lib/systemd/network/99-default.link",
		"usr/lib/systemd/network/README",          // not a unit: ignored
		"usr/lib/systemd/network/80-old.network~", // editor backup: ignored
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("[Match]\n"), 0o644))
	}
	// an absolute symlink target doesn't resolve inside the test (or the
	// agent's container), but the unit must still be listed
	require.NoError(t, os.Symlink("/usr/lib/systemd/network/99-default.link", filepath.Join(root, "etc/systemd/network/50-custom.link")))

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	units, err := networkdUnitFiles(acc)
	require.NoError(t, err)
	// precedence order: /etc, then /run, then /usr/lib
	assert.Equal(t, []string{
		"/etc/systemd/network/10-eth0.network",
		"/etc/systemd/network/50-custom.link",
		"/run/systemd/network/70-eth1.network",
		"/usr/lib/systemd/network/99-default.link",
	}, units)
}

func TestNetworkdUnitFilesNoConfig(t *testing.T) {
	acc, err := NewAccessor(context.Background(), Config{Root: t.TempDir(), Destination: t.TempDir()})
	require.NoError(t, err)

	units, err := networkdUnitFiles(acc)
	require.NoError(t, err)
	assert.Empty(t, units)
}

func TestDumpNetworkdUnits(t *testing.T) {
	bin := t.TempDir()
	// echo the unit argument so the output file records which unit was dumped
	script := `#!/bin/sh
echo "config of $2"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "systemd-analyze"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	dst := t.TempDir()
	// Root must be "/" here: a non-root Root makes osext chroot the command,
	// which an unprivileged test cannot do.
	acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	// 99-default.link was already dumped by the per-interface view, and
	// 10-eth0.network exists in /etc and /usr/lib: the /etc copy overrides it.
	dumped := map[string]bool{"99-default.link": true}
	units := []string{
		"/etc/systemd/network/10-eth0.network",
		"/run/systemd/network/70-eth1.network",
		"/usr/lib/systemd/network/10-eth0.network",
		"/usr/lib/systemd/network/99-default.link",
	}
	require.NoError(t, dumpNetworkdUnits(acc, units, dumped))

	for name, want := range map[string]string{
		"10-eth0.network": "config of /etc/systemd/network/10-eth0.network\n",
		"70-eth1.network": "config of /run/systemd/network/70-eth1.network\n",
	} {
		got, err := os.ReadFile(filepath.Join(dst, "networking/systemd-network", name))
		require.NoError(t, err, name)
		assert.Equal(t, want, string(got), name)
	}
	// already dumped by the per-interface view: not dumped again
	_, err = os.Stat(filepath.Join(dst, "networking/systemd-network/99-default.link"))
	assert.True(t, os.IsNotExist(err), "99-default.link must not be dumped twice")
}

// The file names sort in the opposite order to the directories, so the
// expected order can only come from the directory precedence.
func TestNetworkdUnitFilesDirectoryPrecedence(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"etc/systemd/network/50-etc.network",
		"run/systemd/network/40-run.network",
		"usr/local/lib/systemd/network/30-local.network",
		"usr/lib/systemd/network/20-usrlib.network",
		"lib/systemd/network/10-lib.network",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("[Match]\n"), 0o644))
	}

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	units, err := networkdUnitFiles(acc)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"/etc/systemd/network/50-etc.network",
		"/run/systemd/network/40-run.network",
		"/usr/local/lib/systemd/network/30-local.network",
		"/usr/lib/systemd/network/20-usrlib.network",
		"/lib/systemd/network/10-lib.network",
	}, units)
}

// A directory that can't be read is reported, and the other directories are
// still listed.
func TestNetworkdUnitFilesReadDirError(t *testing.T) {
	root := t.TempDir()
	// a file where /etc/systemd/network should be a directory
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/systemd"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/systemd/network"), []byte("not a directory"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run/systemd/network"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "run/systemd/network/70-eth1.network"), []byte("[Match]\n"), 0o644))

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	units, err := networkdUnitFiles(acc)
	assert.Error(t, err)
	assert.Equal(t, []string{"/run/systemd/network/70-eth1.network"}, units)
}

// One unit that systemd-analyze can't read must not fail the capture or stop
// the units after it from being dumped.
func TestDumpNetworkdUnitsToleratesFailingUnit(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  *bad*) exit 1 ;;
  *) echo "config of $2" ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "systemd-analyze"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	dst := t.TempDir()
	// Root must be "/" here: a non-root Root makes osext chroot the command,
	// which an unprivileged test cannot do.
	acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	units := []string{
		"/etc/systemd/network/10-bad.network",
		"/etc/systemd/network/20-good.network",
	}
	require.NoError(t, dumpNetworkdUnits(acc, units, map[string]bool{}))

	got, err := os.ReadFile(filepath.Join(dst, "networking/systemd-network/20-good.network"))
	require.NoError(t, err)
	assert.Equal(t, "config of /etc/systemd/network/20-good.network\n", string(got))
	_, err = os.Stat(filepath.Join(dst, "networking/systemd-network/10-bad.network"))
	assert.True(t, os.IsNotExist(err), "a failed unit must not leave an output file")
}

// systemdNetworkConfig must keep going when networkctl fails: it records the
// failure and still dumps the unit files. Commands can't run under a temp Root
// (osext chroots them), so networkctl failing is the state under test. The
// dump itself is covered by TestDumpNetworkdUnits.
func TestSystemdNetworkConfigNetworkctlFails(t *testing.T) {
	root, dst := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/systemd/network"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/systemd/network/10-eth0.network"), []byte("[Match]\n"), 0o644))

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, systemdNetworkConfig(acc))

	got, err := os.ReadFile(filepath.Join(dst, "networking/CheckMacAddressPolicy.log"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "Failed to get network interfaces")
	// the output directory is created after the networkctl step, so it only
	// exists if the function carried on past the failure
	assert.DirExists(t, filepath.Join(dst, "networking/systemd-network"))
}
