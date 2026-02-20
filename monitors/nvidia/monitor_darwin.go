//go:build darwin

package nvidia

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

var _ monitor.Monitor = (*NvidiaMonitor)(nil)

func NewNvidiaMonitor() *NvidiaMonitor {
	return &NvidiaMonitor{}
}

type NvidiaMonitor struct{}

func (m *NvidiaMonitor) Name() string {
	return "nvidia"
}

func (m *NvidiaMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *NvidiaMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	// This is a dummy monitor to make the builds succeed on a Mac.
	return nil
}
