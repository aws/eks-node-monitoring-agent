package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProcModules creates <root>/proc/modules with the given contents.
func writeProcModules(t *testing.T, root, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/modules"), []byte(contents), 0o644))
}

func TestLoadedModules(t *testing.T) {
	root := t.TempDir()
	writeProcModules(t, root, `ip_vs_rr 12288 1 - Live 0x0000000000000000
ip_vs 237568 3 ip_vs_rr, Live 0x0000000000000000
nf_conntrack 200704 4 nf_nat,ip_vs, Live 0x0000000000000000
ena 180224 0 - Live 0x0000000000000000
vfat 24576 1 - Live 0x0000000000000000
`)

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, loadedModules(acc))

	got, err := os.ReadFile(filepath.Join(dst, "modinfo/ip_vs"))
	require.NoError(t, err)
	want := `ip_vs_rr 12288 1 - Live 0x0000000000000000
ip_vs 237568 3 ip_vs_rr, Live 0x0000000000000000
nf_conntrack 200704 4 nf_nat,ip_vs, Live 0x0000000000000000
`
	assert.Equal(t, want, string(got))
}

func TestLoadedModulesNoneLoaded(t *testing.T) {
	root := t.TempDir()
	writeProcModules(t, root, "ena 180224 0 - Live 0x0000000000000000\n")

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, loadedModules(acc))

	// the file is written even when empty, so "nothing loaded" is
	// distinguishable from "not collected".
	got, err := os.ReadFile(filepath.Join(dst, "modinfo/ip_vs"))
	require.NoError(t, err)
	assert.Empty(t, string(got))
}
