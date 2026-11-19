package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCachedUdevInterfaces(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "etc/eks/nodeadm/udev-net-manager")
	// two instance-id directories caching an overlapping interface, which must
	// be deduplicated
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "i-0123456789abcdef0"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "i-0fedcba9876543210"), 0o755))
	for _, f := range []string{"i-0123456789abcdef0/ens5", "i-0123456789abcdef0/ens6", "i-0fedcba9876543210/ens5"} {
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, f), nil, 0o644))
	}

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	ifaces, err := cachedUdevInterfaces(acc)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ens5", "ens6"}, ifaces)
}

func TestCachedUdevInterfacesNoCacheDir(t *testing.T) {
	acc, err := NewAccessor(context.Background(), Config{Root: t.TempDir(), Destination: t.TempDir()})
	require.NoError(t, err)

	// hosts without udev-net-manager: nothing discovered, no error
	ifaces, err := cachedUdevInterfaces(acc)
	require.NoError(t, err)
	assert.Empty(t, ifaces)
}

func TestCollectUdevJournals(t *testing.T) {
	bin := t.TempDir()
	// echo the unit argument so the output file records which unit was queried
	script := `#!/bin/sh
echo "journal for $4"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "journalctl"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	dst := t.TempDir()
	// Root must be "/" here: a non-root Root makes osext chroot the command,
	// which an unprivileged test cannot do.
	acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	require.NoError(t, collectUdevJournals(acc, []string{"ens5", "ens6"}))

	for _, iface := range []string{"ens5", "ens6"} {
		got, err := os.ReadFile(filepath.Join(dst, "nodeadm/udev-net-manager_"+iface+".log"))
		require.NoError(t, err, iface)
		assert.Equal(t, "journal for udev-net-manager@"+iface+"\n", string(got), iface)
	}
}
