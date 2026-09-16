package instancestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/util"
	"github.com/go-logr/logr"
	log "sigs.k8s.io/controller-runtime/pkg/log"
)

var _ monitor.Monitor = (*InstanceStoreMonitor)(nil)

// nvmeDevicesGlobPath identifies instance store NVMe devices on AWS EC2
const nvmeDevicesGlobPath = "/dev/disk/by-id/nvme-Amazon_EC2_NVMe_Instance_Storage*"

// requiredMounts are the device-mapper nodes that must exist on nodes with
// instance store devices: containerd image state and kubelet pod data
var requiredMounts = []string{
	"/dev/disk/by-id/dm-name-nvme-containerd_state",
	"/dev/disk/by-id/dm-name-nvme-kubelet_pods",
}

type InstanceStoreMonitor struct {
	manager monitor.Manager
	logger  logr.Logger
}

func (m *InstanceStoreMonitor) Name() string {
	return "instance-store"
}

func (m *InstanceStoreMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *InstanceStoreMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	m.logger = log.FromContext(ctx)

	go util.NewChannelHandler(
		func(time.Time) error { return m.handleMounts() },
		util.TimeTickWithJitterContext(ctx, 5*time.Minute),
	).Start(ctx)

	return nil
}

func (m *InstanceStoreMonitor) handleMounts() error {
	matches, err := filepath.Glob(config.ToHostPath(nvmeDevicesGlobPath))
	if err != nil {
		// the only possible error is ErrBadPattern
		return fmt.Errorf("failed to glob instance store devices: %w", err)
	}
	if len(matches) == 0 {
		m.logger.V(1).Info("no instance store devices found on this node")
		return nil
	}
	for _, mount := range requiredMounts {
		if err := m.checkMount(config.ToHostPath(mount)); err != nil {
			return err
		}
	}
	return nil
}

func (m *InstanceStoreMonitor) checkMount(path string) error {
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to stat instance store mount %s: %w", path, err)
	}
	return m.manager.Notify(context.Background(), monitor.Condition{
		Reason:   "InstanceStoreMountMissing",
		Message:  fmt.Sprintf("Instance store devices are present but mount point %s does not exist", path),
		Severity: monitor.SeverityFatal,
	})
}
