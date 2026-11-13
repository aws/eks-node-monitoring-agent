package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ipvsadm and ipset are not installed on most nodes, so collection must skip
// silently when they are absent and capture their state when present.

func TestIPVSCollection(t *testing.T) {
	t.Run("ipvsadm present collects save, rate and stats", func(t *testing.T) {
		bin := t.TempDir()
		script := `#!/bin/sh
case "$*" in
  --version) echo "ipvsadm v1.31 2019/12/24" ;;
  "--save --numeric") echo "-A -t 10.100.0.1:443 -s rr" ;;
  "--list --numeric --rate") echo "rate table" ;;
  "--list --numeric --stats --exact") echo "stats table" ;;
  *) exit 1 ;;
esac
`
		require.NoError(t, os.WriteFile(filepath.Join(bin, "ipvsadm"), []byte(script), 0o755))
		t.Setenv("PATH", bin)
		stubModuleLoaded(t, true)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipvs(acc))

		got, err := os.ReadFile(filepath.Join(dst, "networking/ipvsadm.txt"))
		require.NoError(t, err)
		want := `*** Output of ipvsadm --save --numeric ***
-A -t 10.100.0.1:443 -s rr
*** Output of ipvsadm --list --numeric --rate ***
rate table
*** Output of ipvsadm --list --numeric --stats --exact ***
stats table
`
		assert.Equal(t, want, string(got))
	})

	t.Run("ipvsadm absent skips without error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		stubModuleLoaded(t, true)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipvs(acc))
		_, err = os.Stat(filepath.Join(dst, "networking/ipvsadm.txt"))
		assert.True(t, os.IsNotExist(err), "no output file expected when ipvsadm is absent")
	})
}

func TestIPSetCollection(t *testing.T) {
	t.Run("ipset present collects list and save", func(t *testing.T) {
		bin := t.TempDir()
		script := `#!/bin/sh
case "$*" in
  version) echo "ipset v7.1, protocol version: 14" ;;
  --list) echo "Name: KUBE-LOOP-BACK" ;;
  --save) echo "create KUBE-LOOP-BACK hash:ip,port,ip" ;;
  *) exit 1 ;;
esac
`
		require.NoError(t, os.WriteFile(filepath.Join(bin, "ipset"), []byte(script), 0o755))
		t.Setenv("PATH", bin)
		stubModuleLoaded(t, true)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipset(acc))

		got, err := os.ReadFile(filepath.Join(dst, "networking/ipset.txt"))
		require.NoError(t, err)
		want := `*** Output of ipset --list ***
Name: KUBE-LOOP-BACK
*** Output of ipset --save ***
create KUBE-LOOP-BACK hash:ip,port,ip
`
		assert.Equal(t, want, string(got))
	})

	t.Run("ipset absent skips without error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		stubModuleLoaded(t, true)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipset(acc))
		_, err = os.Stat(filepath.Join(dst, "networking/ipset.txt"))
		assert.True(t, os.IsNotExist(err), "no output file expected when ipset is absent")
	})
}

// stubModuleLoaded replaces moduleLoaded for the duration of the test, so the
// collectors can run with Root "/" (which keeps commands from being chrooted).
func stubModuleLoaded(t *testing.T, loaded bool) {
	t.Helper()
	orig := moduleLoaded
	moduleLoaded = func(*Accessor, string) bool { return loaded }
	t.Cleanup(func() { moduleLoaded = orig })
}

// A capture must not change kernel state: ipvsadm and ipset must not run at
// all when their module isn't loaded, because ipvsadm modprobes ip_vs and
// ipset can trigger module autoloading.
func TestModuleNotLoadedSkipsCommand(t *testing.T) {
	for _, tc := range []struct {
		bin     string
		collect func(*Accessor) error
	}{
		{bin: "ipvsadm", collect: ipvs},
		{bin: "ipset", collect: ipset},
	} {
		t.Run(tc.bin, func(t *testing.T) {
			bin := t.TempDir()
			marker := filepath.Join(t.TempDir(), "ran")
			require.NoError(t, os.WriteFile(filepath.Join(bin, tc.bin), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755))
			t.Setenv("PATH", bin)
			stubModuleLoaded(t, false)

			acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: t.TempDir()})
			require.NoError(t, err)

			require.NoError(t, tc.collect(acc))
			_, err = os.Stat(marker)
			assert.True(t, os.IsNotExist(err), "%s must not run when its module isn't loaded", tc.bin)
		})
	}
}

func TestModuleLoaded(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/module/ip_vs"), 0o755))

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	assert.True(t, moduleLoaded(acc, "ip_vs"))
	assert.False(t, moduleLoaded(acc, "ip_set"))
}
