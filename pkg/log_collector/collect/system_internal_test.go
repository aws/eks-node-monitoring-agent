package collect

import (
	"context"
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
