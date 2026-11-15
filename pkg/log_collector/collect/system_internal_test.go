package collect

import (
	"bytes"
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

// environ builds /proc/<pid>/environ contents: count NUL-terminated variables.
func environ(count int) []byte {
	var env []byte
	for i := 0; i < count; i++ {
		env = append(env, []byte(fmt.Sprintf("VAR_%d=value", i))...)
		env = append(env, 0)
	}
	return env
}

func collectLargeEnvironments(t *testing.T, root string) string {
	t.Helper()
	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)
	require.NoError(t, largeEnvironments(acc))
	got, err := os.ReadFile(filepath.Join(dst, "system/large_environments.txt"))
	require.NoError(t, err)
	return string(got)
}

func writeProcFile(t *testing.T, root, dir string, data []byte) {
	t.Helper()
	path := filepath.Join(root, "proc", dir)
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "environ"), data, 0o644))
}

func TestLargeEnvironments(t *testing.T) {
	root := t.TempDir()
	writeProcFile(t, root, "100", environ(1001)) // over the threshold: reported
	writeProcFile(t, root, "200", environ(5))    // normal: not reported
	writeProcFile(t, root, "300", environ(1000)) // exactly the threshold: not reported (script uses >1000)

	assert.Equal(t, "PID\tCount\n100\t1001\n", collectLargeEnvironments(t, root))
}

func TestLargeEnvironmentsEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		procs map[string][]byte // dir under /proc -> environ contents
		want  string
	}{
		{
			name:  "nothing over the threshold writes only the header",
			procs: map[string][]byte{"100": environ(5), "200": environ(1000)},
			want:  "PID\tCount\n",
		},
		{
			name:  "no processes writes only the header",
			procs: map[string][]byte{},
			want:  "PID\tCount\n",
		},
		{
			name:  "empty environ (kernel thread) is not reported",
			procs: map[string][]byte{"2": {}},
			want:  "PID\tCount\n",
		},
		{
			name: "multiple reported PIDs are in glob (lexical) order",
			procs: map[string][]byte{
				"200":  environ(1002),
				"1000": environ(1001),
				"30":   environ(5),
			},
			want: "PID\tCount\n1000\t1001\n200\t1002\n",
		},
		{
			name: "last variable without a trailing NUL still counts",
			// 1000 terminated entries plus one unterminated entry is 1001
			procs: map[string][]byte{"100": append(environ(1000), []byte("LAST=value")...)},
			want:  "PID\tCount\n100\t1001\n",
		},
		{
			name: "empty entries between NULs are not counted",
			procs: map[string][]byte{
				// 1001 real variables, each followed by an extra empty entry
				"100": bytes.ReplaceAll(environ(1001), []byte{0}, []byte{0, 0}),
				// 600 real variables padded with 600 empty entries: 1200 pieces, 600 real
				"200": bytes.ReplaceAll(environ(600), []byte{0}, []byte{0, 0}),
			},
			want: "PID\tCount\n100\t1001\n",
		},
		{
			name: "non-PID entries under /proc are ignored",
			procs: map[string][]byte{
				"self": environ(2000),
				"sys":  environ(2000),
				"100":  environ(1001),
			},
			want: "PID\tCount\n100\t1001\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
			for dir, data := range tc.procs {
				writeProcFile(t, root, dir, data)
			}
			assert.Equal(t, tc.want, collectLargeEnvironments(t, root))
		})
	}
}

// an unreadable environ (another user's process) is skipped, not an error.
func TestLargeEnvironmentsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a chmod 000 file, so the file is not unreadable")
	}
	root := t.TempDir()
	writeProcFile(t, root, "100", environ(1001))
	writeProcFile(t, root, "400", environ(2000))
	require.NoError(t, os.Chmod(filepath.Join(root, "proc", "400", "environ"), 0o000))

	assert.Equal(t, "PID\tCount\n100\t1001\n", collectLargeEnvironments(t, root))
}
