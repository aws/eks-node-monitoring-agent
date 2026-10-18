package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadedModules(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
echo "Module                  Size  Used by"
echo "ip_vs_rr               12288  1"
echo "ip_vs                 237568  3 ip_vs_rr"
echo "nf_conntrack          200704  4 nf_nat,ip_vs"
echo "ena                   180224  0"
echo "vfat                   24576  1"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "lsmod"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	require.NoError(t, loadedModules(acc))

	got, err := os.ReadFile(filepath.Join(dst, "modinfo/ip_vs"))
	require.NoError(t, err)
	want := `ip_vs_rr               12288  1
ip_vs                 237568  3 ip_vs_rr
nf_conntrack          200704  4 nf_nat,ip_vs
`
	assert.Equal(t, want, string(got))
}

func TestLoadedModulesNoneLoaded(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
echo "Module                  Size  Used by"
echo "ena                   180224  0"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "lsmod"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
	require.NoError(t, err)

	require.NoError(t, loadedModules(acc))

	// the file is written even when empty, so "nothing loaded" is
	// distinguishable from "not collected".
	got, err := os.ReadFile(filepath.Join(dst, "modinfo/ip_vs"))
	require.NoError(t, err)
	assert.Empty(t, string(got))
}
