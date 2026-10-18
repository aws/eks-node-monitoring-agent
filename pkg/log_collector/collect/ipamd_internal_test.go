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

// Newer VPC CNI versions keep more than ipam.json in the state directory; the
// whole directory's regular files are the checkpoint, while sockets and
// subdirectories must be skipped.
func TestIPAMDDatastores(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "var/run/aws-node")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "ipam.json"), []byte(`{"allocations":[]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "eni.json"), []byte(`{"enis":[]}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "subdir"), 0o755))

	// the directory also holds the ipamd unix sockets; a FIFO stands in for
	// them here as the non-regular file that must be skipped
	require.NoError(t, syscall.Mkfifo(filepath.Join(stateDir, "ipamd.sock"), 0o644))

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, ipamdDatastores(acc))

	for file, want := range map[string]string{
		"ipamd/ipam.json": `{"allocations":[]}`,
		"ipamd/eni.json":  `{"enis":[]}`,
	} {
		got, err := os.ReadFile(filepath.Join(dst, file))
		require.NoError(t, err, file)
		assert.Equal(t, want, string(got), file)
	}
	for _, absent := range []string{"ipamd/ipamd.sock", "ipamd/subdir"} {
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
