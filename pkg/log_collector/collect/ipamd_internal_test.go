package collect

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IPAMD keeps one datastore per network card in the state directory; all its
// regular files are collected, while sockets, symlinks and subdirectories must
// be skipped.
func TestIPAMDDatastores(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "var/run/aws-node")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "ipam.json"), []byte(`{"allocations":[]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "ipam-nic-1.json"), []byte(`{"allocations":["nic1"]}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "subdir"), 0o755))

	// a symlink must not be followed: it could point anywhere on the host
	secret := filepath.Join(root, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("not for the bundle"), 0o600))
	require.NoError(t, os.Symlink(secret, filepath.Join(stateDir, "link.json")))

	// the directory also holds the ipamd unix sockets; a FIFO stands in for
	// them here as the non-regular file that must be skipped
	require.NoError(t, syscall.Mkfifo(filepath.Join(stateDir, "ipamd.sock"), 0o644))

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, ipamdDatastores(acc))

	for file, want := range map[string]string{
		"ipamd/ipam.json":       `{"allocations":[]}`,
		"ipamd/ipam-nic-1.json": `{"allocations":["nic1"]}`,
	} {
		got, err := os.ReadFile(filepath.Join(dst, file))
		require.NoError(t, err, file)
		assert.Equal(t, want, string(got), file)
	}
	for _, absent := range []string{"ipamd/ipamd.sock", "ipamd/subdir", "ipamd/link.json"} {
		_, err := os.Stat(filepath.Join(dst, absent))
		assert.True(t, os.IsNotExist(err), "%s must not be collected", absent)
	}
}

func TestIPAMDDatastoresMissingDirectory(t *testing.T) {
	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: t.TempDir(), Destination: dst})
	require.NoError(t, err)

	// no VPC CNI on the node: not an error, nothing collected
	require.NoError(t, ipamdDatastores(acc))
	entries, err := os.ReadDir(dst)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestIPAMDDatastoresBottlerocket(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "run/aws-node")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "ipam.json"), []byte(`{"allocations":[]}`), 0o644))

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst, Tags: []string{TagBottlerocket}})
	require.NoError(t, err)

	require.NoError(t, ipamdDatastores(acc))
	got, err := os.ReadFile(filepath.Join(dst, "ipamd/ipam.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"allocations":[]}`, string(got))
}
