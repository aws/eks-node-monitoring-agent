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
  --save) echo "-A -t 10.100.0.1:443 -s rr" ;;
  "--list --numeric --rate") echo "rate table" ;;
  "--list --numeric --stats --exact") echo "stats table" ;;
  *) exit 1 ;;
esac
`
		require.NoError(t, os.WriteFile(filepath.Join(bin, "ipvsadm"), []byte(script), 0o755))
		t.Setenv("PATH", bin)

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipvs(acc))

		got, err := os.ReadFile(filepath.Join(dst, "networking/ipvsadm.txt"))
		require.NoError(t, err)
		want := `*** Output of ipvsadm --save ***
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

		dst := t.TempDir()
		acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
		require.NoError(t, err)

		require.NoError(t, ipset(acc))
		_, err = os.Stat(filepath.Join(dst, "networking/ipset.txt"))
		assert.True(t, os.IsNotExist(err), "no output file expected when ipset is absent")
	})
}
