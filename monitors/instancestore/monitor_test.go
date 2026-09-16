package instancestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockManager struct {
	conditions []monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return nil, nil
}

func (m *mockManager) Notify(_ context.Context, c monitor.Condition) error {
	m.conditions = append(m.conditions, c)
	return nil
}

// setupHostRoot creates a host root with the given dev/disk/by-id entries
func setupHostRoot(t *testing.T, entries ...string) {
	t.Helper()
	root := t.TempDir()
	for _, entry := range entries {
		path := filepath.Join(root, entry)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte{}, 0o644))
	}
	t.Setenv("HOST_ROOT", root)
}

func TestHandleMounts(t *testing.T) {
	tests := []struct {
		name           string
		entries        []string
		wantConditions int
	}{
		{
			name:           "no instance store devices",
			entries:        nil,
			wantConditions: 0,
		},
		{
			name: "devices and mounts present",
			entries: []string{
				"dev/disk/by-id/nvme-Amazon_EC2_NVMe_Instance_Storage_AAAA1234",
				"dev/disk/by-id/dm-name-nvme-containerd_state",
				"dev/disk/by-id/dm-name-nvme-kubelet_pods",
			},
			wantConditions: 0,
		},
		{
			name: "kubelet pods mount missing",
			entries: []string{
				"dev/disk/by-id/nvme-Amazon_EC2_NVMe_Instance_Storage_AAAA1234",
				"dev/disk/by-id/dm-name-nvme-containerd_state",
			},
			wantConditions: 1,
		},
		{
			name: "all mounts missing",
			entries: []string{
				"dev/disk/by-id/nvme-Amazon_EC2_NVMe_Instance_Storage_AAAA1234",
			},
			wantConditions: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHostRoot(t, tt.entries...)
			mgr := &mockManager{}
			m := &InstanceStoreMonitor{manager: mgr, logger: logr.Discard()}

			require.NoError(t, m.handleMounts())
			assert.Len(t, mgr.conditions, tt.wantConditions)
			for _, c := range mgr.conditions {
				assert.Equal(t, "InstanceStoreMountMissing", c.Reason)
				assert.Equal(t, monitor.SeverityFatal, c.Severity)
			}
		})
	}
}
