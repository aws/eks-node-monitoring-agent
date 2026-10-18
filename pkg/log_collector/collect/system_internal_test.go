package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// procps 4.0.7 made 'sysctl --all' exit 1 when any key cannot be read, which
// failed every capture on hosts with unset ipv6 stable_secret keys.
func TestSysctlPartialReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
		want    string
	}{
		{
			name:   "exit 1 with output keeps the dump",
			script: "echo 'kernel.ostype = Linux'; exit 1",
			want:   "kernel.ostype = Linux\n",
		},
		{
			name:    "exit 1 without output is an error",
			script:  "exit 1",
			wantErr: true,
		},
		{
			name:    "other exit codes are an error",
			script:  "echo 'kernel.ostype = Linux'; exit 2",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "sysctl"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755))
			t.Setenv("PATH", bin)

			dst := t.TempDir()
			acc, err := NewAccessor(context.Background(), Config{Root: "/", Destination: dst})
			require.NoError(t, err)

			err = sysctl(acc)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(dst, "sysctls/sysctl_all.txt"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestLargeEnvironments(t *testing.T) {
	root := t.TempDir()
	writeEnviron := func(pid string, count int) {
		dir := filepath.Join(root, "proc", pid)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		var env []byte
		for i := 0; i < count; i++ {
			env = append(env, []byte(fmt.Sprintf("VAR_%d=value", i))...)
			env = append(env, 0)
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "environ"), env, 0o644))
	}
	writeEnviron("100", 1001) // over the threshold: reported
	writeEnviron("200", 5)    // normal: not reported
	writeEnviron("300", 1000) // exactly the threshold: not reported (script uses >1000)

	// unreadable environ (another user's process): skipped, not an error
	writeEnviron("400", 2000)
	require.NoError(t, os.Chmod(filepath.Join(root, "proc", "400", "environ"), 0o000))

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, largeEnvironments(acc))

	got, err := os.ReadFile(filepath.Join(dst, "system/large_environments.txt"))
	require.NoError(t, err)
	assert.Equal(t, "PID\tCount\n100\t1001\n", string(got))
}
