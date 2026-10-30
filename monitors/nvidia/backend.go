//go:build !darwin

package nvidia

import (
	"context"
	"os"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/nvml"
)

// backend is the library the nvidia monitor reads GPU health through.
type backend string

const (
	// backendDCGM reads GPU health from a DCGM hostengine.
	backendDCGM backend = "dcgm"
	// backendNVML reads GPU health from NVML, the NVIDIA driver's management
	// library, in the agent process.
	backendNVML backend = "nvml"
)

// defaultBackend is the backend this release reads GPU health through.
const defaultBackend = backendDCGM

// backendEnv overrides defaultBackend, for tests and for pinning the backend
// on a host. It is not a supported configuration setting.
const backendEnv = "NMA_NVIDIA_BACKEND"

// selectBackend returns the backend backendEnv names. An unset or empty value
// is the default. Any other value is also the default, and unknown is true so
// the caller can log it.
func selectBackend(value string, set bool) (b backend, unknown bool) {
	switch backend(value) {
	case backendDCGM, backendNVML:
		return backend(value), false
	}
	return defaultBackend, set && value != ""
}

// backend reads backendEnv once, when the monitor registers.
func (m *nvidiaMonitor) backend(ctx context.Context) backend {
	logger := log.FromContext(ctx)
	value, set := os.LookupEnv(backendEnv)
	b, unknown := selectBackend(value, set)
	if unknown {
		logger.Info("ignoring an unknown NVIDIA backend", "env", backendEnv, "value", value, "backend", b)
	}
	logger.Info("reading NVIDIA GPU health", "backend", b)
	return b
}

// nvmlChecks are the NVML reader and its checks, as the monitor runs them.
// Each runs from its own goroutine. A poll that read nothing returns no
// conditions; the reader's Reconcile reports a driver that stops answering.
type nvmlChecks struct {
	reconcile func(context.Context) []monitor.Condition
	policies  func() ([]monitor.Condition, bool)
	devices   func(context.Context) ([]monitor.Condition, bool)
	health    func() ([]monitor.Condition, bool)
	xids      func(context.Context) ([]monitor.Condition, bool)
}

// nvmlConfig is what the monitor passes to the NVML checks.
type nvmlConfig struct {
	gracePeriod         time.Duration
	powerThresholdWatts uint32
	instanceInfo        instanceinfo.InstanceTypeInfoProvider
}

// newNVMLChecks creates the NVML reader and registers every check with it.
// The checks must all exist before the reader's first Reconcile.
func newNVMLChecks(cfg nvmlConfig) nvmlChecks {
	reader := nvml.NewReader(nvml.Config{GracePeriod: cfg.gracePeriod})
	policies := nvml.NewPolicies(reader)
	policies.SetPowerThresholdWatts(cfg.powerThresholdWatts)
	devices := nvml.NewDevices(reader, cfg.instanceInfo)
	health := nvml.NewHealth(reader)
	xids := nvml.NewXIDs(reader)
	return nvmlChecks{
		reconcile: reader.Reconcile,
		policies:  policies.Poll,
		devices:   devices.Poll,
		health:    health.Poll,
		xids:      xids.Wait,
	}
}

// registerNVML reads GPU health through NVML. The DCGM client is never
// started.
func (m *nvidiaMonitor) registerNVML(ctx context.Context, mgr monitor.Manager) {
	checks := m.newNVMLChecks(nvmlConfig{
		gracePeriod:         dcgmClientInializationGracePeriod,
		powerThresholdWatts: m.powerThresholdWatts,
		instanceInfo:        m.instanceTypeInfoProvider,
	})

	// Every 30 seconds: load and initialize NVML, and report a driver that
	// stops answering.
	go func() {
		for range m.tickFunc(ctx, 30*time.Second) {
			notifyAll(ctx, mgr, "NVML reconcile", checks.reconcile(ctx))
		}
	}()

	// The policy checks, at the rate DCGM samples its policy fields.
	go func() {
		for range m.tickFunc(ctx, nvml.PolicyInterval) {
			conds, _ := checks.policies()
			notifyAll(ctx, mgr, "NVML policy", conds)
		}
	}()

	// GPU count, fabric state and lost GPUs.
	go func() {
		for range m.tickFunc(ctx, nvml.DevicesInterval) {
			conds, _ := checks.devices(ctx)
			notifyAll(ctx, mgr, "NVML device", conds)
		}
	}()

	// The DCGM health-watch codes.
	go func() {
		for range m.tickFunc(ctx, nvml.HealthInterval) {
			conds, _ := checks.health()
			notifyAll(ctx, mgr, "NVML health", conds)
		}
	}()

	// XID events. Each wait returns within a few seconds, and pauses when it
	// can't wait in the driver, so the loop doesn't spin.
	go func() {
		for ctx.Err() == nil {
			conds, _ := checks.xids(ctx)
			notifyAll(ctx, mgr, "NVML XID", conds)
		}
	}()
}

// notifyAll sends each condition to the manager and logs the ones it can't
// send.
func notifyAll(ctx context.Context, mgr monitor.Manager, source string, conds []monitor.Condition) {
	for _, c := range conds {
		if err := mgr.Notify(ctx, c); err != nil {
			log.FromContext(ctx).Error(err, "failed to notify condition", "source", source, "reason", c.Reason)
		}
	}
}
