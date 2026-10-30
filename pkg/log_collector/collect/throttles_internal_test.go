package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCgroup(t *testing.T, root, name, nrThrottled, procs string) {
	t.Helper()
	dir := filepath.Join(root, "sys/fs/cgroup", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stat := "nr_periods 100\nnr_throttled " + nrThrottled + "\nthrottled_time 5000\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte(stat), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(procs), 0o644))
}

func TestGatherThrottledCgroups(t *testing.T) {
	root := t.TempDir()
	// throttled, with PIDs; cgroup.procs ends with a newline like the real
	// file — the trailing empty entry must not become a PID, and neither must
	// the "0" the kernel renders for PIDs outside the reader's PID namespace
	writeCgroup(t, root, "kubepods/burstable/podA", "3", "123\n0\n456\n")
	// not throttled: excluded
	writeCgroup(t, root, "kubepods/burstable/podB", "0", "789\n")
	// throttled but empty (all processes exited): excluded
	writeCgroup(t, root, "kubepods/burstable/podC", "7", "\n")

	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: t.TempDir()})
	require.NoError(t, err)

	cgroups, merr := gatherThrottledCgroups(acc)
	require.NoError(t, merr)
	require.Len(t, cgroups, 1)
	assert.Equal(t, "/sys/fs/cgroup/kubepods/burstable/podA", cgroups[0].path)
	assert.Equal(t, []string{"123", "456"}, cgroups[0].pids)
	assert.Contains(t, cgroups[0].stat, "nr_throttled 3")
	assert.Contains(t, cgroups[0].stat, "throttled_time 5000")
}

func TestRenderThrottles(t *testing.T) {
	// ps ax right-aligns the PID column; PID 42 must match only its own line,
	// not the line of PID 4231 that begins with the same digits.
	psOutput := `    PID TTY      STAT   TIME COMMAND
     42 ?        Ss     0:01 /usr/bin/throttled-app
   4231 ?        Sl     9:59 /usr/bin/innocent-bystander
    456 ?        Ssl    1:23 /usr/bin/other-throttled
`
	cgroups := []throttledCgroup{
		{
			path: "/sys/fs/cgroup/kubepods/podA",
			stat: "nr_periods 100\nnr_throttled 3\nthrottled_time 5000\n",
			pids: []string{"42", "99999"}, // 99999 exited before ps ran
		},
		{
			path: "/sys/fs/cgroup/kubepods/podB",
			stat: "nr_periods 50\nnr_throttled 1\nthrottled_time 200\n",
			pids: []string{"456"},
		},
	}

	got := string(renderThrottles(cgroups, psOutput))

	want := `/sys/fs/cgroup/kubepods/podA
nr_periods 100
nr_throttled 3
throttled_time 5000
     42 ?        Ss     0:01 /usr/bin/throttled-app

/sys/fs/cgroup/kubepods/podB
nr_periods 50
nr_throttled 1
throttled_time 200
    456 ?        Ssl    1:23 /usr/bin/other-throttled

`
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "innocent-bystander")
}

func TestCPUThrottlesNoneFound(t *testing.T) {
	root := t.TempDir()
	writeCgroup(t, root, "kubepods/podA", "0", "123\n")

	dst := t.TempDir()
	acc, err := NewAccessor(context.Background(), Config{Root: root, Destination: dst})
	require.NoError(t, err)

	require.NoError(t, cpuThrottles(acc))
	got, err := os.ReadFile(filepath.Join(dst, "system/cpu_throttling.txt"))
	require.NoError(t, err)
	assert.Equal(t, "No CPU Throttling Found", string(got))
}
