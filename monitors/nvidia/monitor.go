//go:build !darwin

package nvidia

import (
	"context"
	"os"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/nccl"
	"github.com/aws/eks-node-monitoring-agent/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ monitor.Monitor = (*nvidiaMonitor)(nil)

var dcgmClientInializationGracePeriod = time.Minute

func init() {
	if durationStr, ok := os.LookupEnv("DCGM_GRACE_PERIOD_DURATION"); ok {
		duration, err := time.ParseDuration(durationStr)
		if err == nil {
			dcgmClientInializationGracePeriod = duration
		}
	}
}

func NewNvidiaMonitor() *nvidiaMonitor {
	return &nvidiaMonitor{
		dcgmClient: dcgm.NewDCGM(dcgm.DCGMConfig{
			InitializationGracePeriod: dcgmClientInializationGracePeriod,
			Features: []dcgm.Feature{
				dcgm.FeatureActiveDiagnostics,
				dcgm.FeatureFields,
				dcgm.FeatureHealthSystems,
				dcgm.FeaturePolicyViolations,
			},
		}),
		tickFunc:                 util.TimeTickWithJitterContext,
		instanceTypeInfoProvider: instanceinfo.NewInstanceTypeInfoProvider(),
	}
}

// NewNvidiaMonitorWithDeps creates an nvidiaMonitor with injectable dependencies for testing.
func NewNvidiaMonitorWithDeps(dcgmClient dcgm.DCGM, tickFunc TickFunc, provider instanceinfo.InstanceTypeInfoProvider) *nvidiaMonitor {
	return &nvidiaMonitor{
		dcgmClient:               dcgmClient,
		tickFunc:                 tickFunc,
		instanceTypeInfoProvider: provider,
	}
}

// TickFunc is a function that returns a channel that fires periodically.
// It matches the signature of util.TimeTickWithJitterContext.
type TickFunc func(ctx context.Context, d time.Duration) <-chan time.Time

// nvidiaMonitor detects issues on nvidia GPUs
type nvidiaMonitor struct {
	dcgmClient               dcgm.DCGM
	tickFunc                 TickFunc
	instanceTypeInfoProvider instanceinfo.InstanceTypeInfoProvider
}

func (m *nvidiaMonitor) Name() string {
	return "nvidia"
}

func (m *nvidiaMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *nvidiaMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	logger := log.FromContext(ctx)

	dcgmSystem := dcgm.NewDCGMSystemWithInstanceTypeInfoProvider(m.dcgmClient, dcgm.GetDiagType(), m.instanceTypeInfoProvider)

	// DCGM Reconcile - maintains connection to DCGM host
	go func() {
		failed := false
		for range m.tickFunc(ctx, 30*time.Second) {
			conditions, err := dcgmSystem.Reconcile(ctx)
			if err != nil {
				logger.Error(err, "failed to reconcile DCGM")
				continue
			}
			if len(conditions) == 0 {
				// reset the latched fatal condition once DCGM is reachable again
				if failed {
					if err := mgr.Recovered(ctx); err != nil {
						logger.Error(err, "failed to notify DCGM recovery")
						continue
					}
					failed = false
				}
				continue
			}
			failed = true
			for _, condition := range conditions {
				if err := mgr.Notify(ctx, condition); err != nil {
					logger.Error(err, "failed to notify DCGM reconcile condition")
				}
			}
		}
	}()

	// DCGM Active Diagnostics
	go func() {
		for range m.tickFunc(ctx, 5*time.Minute) {
			conditions, err := dcgmSystem.ActiveDiagnostic(ctx)
			if err != nil {
				logger.Error(err, "failed to run DCGM active diagnostics")
				continue
			}
			for _, condition := range conditions {
				if err := mgr.Notify(ctx, condition); err != nil {
					logger.Error(err, "failed to notify DCGM diagnostic condition")
				}
			}
		}
	}()

	// DCGM Policy Violations - continuous monitoring
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				conditions, err := dcgmSystem.Policies(ctx)
				if err != nil {
					logger.Error(err, "failed to check DCGM policies")
					continue
				}
				for _, condition := range conditions {
					if err := mgr.Notify(ctx, condition); err != nil {
						logger.Error(err, "failed to notify DCGM policy condition")
					}
				}
			}
		}
	}()

	// DCGM Health Check
	go func() {
		for range m.tickFunc(ctx, 5*time.Minute) {
			conditions, err := dcgmSystem.HealthCheck(ctx)
			if err != nil {
				logger.Error(err, "failed to run DCGM health check")
				continue
			}
			for _, condition := range conditions {
				if err := mgr.Notify(ctx, condition); err != nil {
					logger.Error(err, "failed to notify DCGM health condition")
				}
			}
		}
	}()

	// DCGM Watch Fields
	go func() {
		for range m.tickFunc(ctx, 5*time.Minute) {
			conditions, err := dcgmSystem.WatchFields(ctx)
			if err != nil {
				logger.Error(err, "failed to watch DCGM fields")
				continue
			}
			for _, condition := range conditions {
				if err := mgr.Notify(ctx, condition); err != nil {
					logger.Error(err, "failed to notify DCGM field condition")
				}
			}
		}
	}()

	// DCGM Device Count
	go func() {
		for range m.tickFunc(ctx, 5*time.Minute) {
			conditions, err := dcgmSystem.DeviceCount(ctx)
			if err != nil {
				logger.Error(err, "failed to check DCGM device count")
				continue
			}
			for _, condition := range conditions {
				if err := mgr.Notify(ctx, condition); err != nil {
					logger.Error(err, "failed to notify DCGM device count condition")
				}
			}
		}
	}()

	// NCCL error monitoring from dmesg
	kmsg, err := mgr.Subscribe(resource.ResourceTypeDmesg, []resource.Part{})
	if err != nil {
		return err
	}
	ncclSystem := nccl.NewNCCLSystem(kmsg)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				conditions, err := ncclSystem.Step(ctx)
				if err != nil {
					logger.Error(err, "failed to check NCCL errors")
					continue
				}
				for _, condition := range conditions {
					if err := mgr.Notify(ctx, condition); err != nil {
						logger.Error(err, "failed to notify NCCL condition")
					}
				}
			}
		}
	}()

	return nil
}
