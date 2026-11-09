package nitrosandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

const (
	testShortA = "aaaaaaaaaaaa"
	testShortB = "bbbbbbbbbbbb"
	testFullA  = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
)

func stale(shortID string) diskSandbox {
	return diskSandbox{
		id:      shortID,
		shortID: shortID,
		modTime: time.Now().Add(-time.Hour),
	}
}

// leakMonitor builds a monitor whose disk, runtime and slot budget are injected:
// two sandboxes on disk, none known to the runtime, and a budget of two, which
// is the exhausted case.
func leakMonitor(mgr monitor.Manager) *nitroSandboxMonitor {
	m := testMonitor(mgr, healthy())
	m.diskSandboxes = func() ([]diskSandbox, error) {
		return []diskSandbox{stale(testShortA), stale(testShortB)}, nil
	}
	m.criSandboxIDs = func(context.Context) ([]string, error) { return nil, nil }
	m.slotBudget = func() (int, error) { return 2, nil }
	return m
}

func TestCheckLeakedSandboxes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*nitroSandboxMonitor)
		wantReport bool
		wantErr    bool
	}{
		{
			name:       "every slot held by a sandbox with no pod is reported",
			wantReport: true,
		},
		{
			name: "a sandbox the runtime still knows about is not leaked",
			mutate: func(m *nitroSandboxMonitor) {
				m.criSandboxIDs = func(context.Context) ([]string, error) {
					return []string{testFullA}, nil
				}
			},
		},
		{
			name: "leaks below the slot budget are not reported",
			mutate: func(m *nitroSandboxMonitor) {
				m.slotBudget = func() (int, error) { return 8, nil }
			},
		},
		{
			name: "sandboxes inside the grace period are not counted",
			mutate: func(m *nitroSandboxMonitor) {
				m.diskSandboxes = func() ([]diskSandbox, error) {
					return []diskSandbox{
						{id: testShortA, shortID: testShortA, modTime: time.Now()},
						{id: testShortB, shortID: testShortB, modTime: time.Now()},
					}, nil
				}
			},
		},
		{
			name: "an unreadable runtime list errors instead of reporting",
			// This is the case where every sandbox on disk would look leaked, so
			// it must never become a node condition.
			mutate: func(m *nitroSandboxMonitor) {
				m.criSandboxIDs = func(context.Context) ([]string, error) {
					return nil, errors.New("cri is down")
				}
			},
			wantErr: true,
		},
		{
			name: "an unreadable slot budget errors instead of reporting",
			mutate: func(m *nitroSandboxMonitor) {
				m.slotBudget = func() (int, error) { return 0, errors.New("no sysfs") }
			},
			wantErr: true,
		},
		{
			name: "a node without sandbox hardware reports nothing",
			mutate: func(m *nitroSandboxMonitor) {
				m.fileExists = func(string) bool { return false }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &mockManager{}
			m := leakMonitor(mgr)
			if tc.mutate != nil {
				tc.mutate(m)
			}

			err := m.checkLeakedSandboxes(context.Background())
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				// Reporting needs two consecutive polls, so the case only
				// reaches the manager on the second.
				require.NoError(t, err)
				require.NoError(t, m.checkLeakedSandboxes(context.Background()))
			}

			if !tc.wantReport {
				assert.Empty(t, mgr.res)
				return
			}
			require.NotEmpty(t, mgr.res)
			assert.Equal(t, "LeakedSandboxResources", mgr.res[0].Reason)
			assert.Equal(t, monitor.SeverityFatal, mgr.res[0].Severity)
			assert.Contains(t, mgr.res[0].Message, "2 of 2")
		})
	}
}

func TestCheckLeakedSandboxesRequiresTwoPolls(t *testing.T) {
	mgr := &mockManager{}
	m := leakMonitor(mgr)

	require.NoError(t, m.checkLeakedSandboxes(context.Background()))
	assert.Empty(t, mgr.res, "first poll should not report")

	require.NoError(t, m.checkLeakedSandboxes(context.Background()))
	assert.Len(t, mgr.res, 1)
}

func TestLeakMessageSeparatesRunningVMs(t *testing.T) {
	running := stale(testShortA)
	running.vmAlive = true

	assert.Contains(t, leakMessage([]diskSandbox{running, stale(testShortB)}, 2), "1 with a running microVM")
	assert.Contains(t, leakMessage([]diskSandbox{stale(testShortA)}, 1), "none still running")
}

// The data directory holds caches and the jailer base alongside sandboxes, so
// only id-shaped names may be counted.
func TestHostDiskSandboxesIgnoresNonSandboxEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOST_ROOT", root)

	data := filepath.Join(root, defaultDataBaseDir)
	for _, dir := range []string{"erofs-cache", "uid-locks", jailDirName, testShortA} {
		require.NoError(t, os.MkdirAll(filepath.Join(data, dir), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(data, jailDirName, testShortB), 0o755))

	state := filepath.Join(root, runStateDir)
	require.NoError(t, os.MkdirAll(filepath.Join(state, testFullA), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(state, testFullA, vmPidFile),
		[]byte(`{"pid":4294967000,"inode_id":1}`), 0o644))

	sandboxes, err := hostDiskSandboxes()
	require.NoError(t, err)

	ids := map[string]diskSandbox{}
	for _, sandbox := range sandboxes {
		ids[sandbox.shortID] = sandbox
	}
	require.Len(t, sandboxes, 2, "only the two id-shaped entries are sandboxes: %v", ids)
	assert.Contains(t, ids, testShortA)
	assert.Contains(t, ids, testShortB)
	assert.Equal(t, testFullA, ids[testShortA].id, "the full id from the state directory wins")
	assert.False(t, ids[testShortA].vmAlive, "a pid that does not exist is not alive")
}

// The walk must follow data_base_dir, or it is blind to the leaks a mismatched
// base directory produces.
func TestResolveDataBaseDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOST_ROOT", root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(shimConfigPath)), 0o755))
	cfg := filepath.Join(root, shimConfigPath)

	assert.Equal(t, defaultDataBaseDir, resolveDataBaseDir(), "no config falls back to the default")

	require.NoError(t, os.WriteFile(cfg, []byte("cpu_overcommit = true\n"), 0o644))
	assert.Equal(t, defaultDataBaseDir, resolveDataBaseDir(), "config without the key falls back")

	require.NoError(t, os.WriteFile(cfg, []byte(`data_base_dir = "/mnt/data/nitro-sandbox"`+"\n"), 0o644))
	assert.Equal(t, "/mnt/data/nitro-sandbox", resolveDataBaseDir())
}

func TestHostSlotBudget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOST_ROOT", root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(pcieSlots)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, pcieSlots), []byte("36\n"), 0o644))

	budget, err := hostSlotBudget()
	require.NoError(t, err)
	assert.Equal(t, 36, budget)
}

func TestVMProcessAlive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, vmPidFile)

	// This process, with the inode the kernel reports for it.
	require.NoError(t, os.WriteFile(path, []byte(`{"pid":1,"inode_id":1}`), 0o644))
	assert.False(t, vmProcessAlive(path), "an inode that does not match means the pid was reused")

	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))
	assert.False(t, vmProcessAlive(path))

	assert.False(t, vmProcessAlive(filepath.Join(dir, "missing")))
}
