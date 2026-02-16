//go:build !darwin

package nvidia

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/nccl"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
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
		sysInfo:  &sysInfo{},
		tickFunc: util.TimeTickWithJitterContext,
	}
}

// NewNvidiaMonitorWithDeps creates an nvidiaMonitor with injectable dependencies for testing.
func NewNvidiaMonitorWithDeps(dcgmClient dcgm.DCGM, sys SysInfo, tickFunc TickFunc) *nvidiaMonitor {
	return &nvidiaMonitor{
		dcgmClient: dcgmClient,
		sysInfo:    sys,
		tickFunc:   tickFunc,
	}
}

// TickFunc is a function that returns a channel that fires periodically.
// It matches the signature of util.TimeTickWithJitterContext.
type TickFunc func(ctx context.Context, d time.Duration) <-chan time.Time

// nvidiaMonitor detects issues on nvidia GPUs
type nvidiaMonitor struct {
	dcgmClient dcgm.DCGM
	sysInfo    SysInfo
	tickFunc   TickFunc
}

func (m *nvidiaMonitor) Name() string {
	return "nvidia"
}

func (m *nvidiaMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *nvidiaMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	logger := log.FromContext(ctx)

	// TODO: until a dcgm-server manifest that contains arm64 images can be
	// provided by the eks-node-monitoring agent chart/addon we are
	// disabling this detection.
	if !slices.Contains(config.GetRuntimeContext().Tags(), config.EKSAuto) && strings.Contains(m.sysInfo.Arch(), "arm") {
		logger.Info("NVIDIA-based monitoring is disabled for the arm64 architecture in this version of the agent")
	} else {
		dcgmSystem := dcgm.NewDCGMSystem(m.dcgmClient, dcgm.GetDiagType())

		// DCGM Reconcile - maintains connection to DCGM host
		go func() {
			for range m.tickFunc(ctx, 30*time.Second) {
				conditions, err := dcgmSystem.Reconcile(ctx)
				if err != nil {
					logger.Error(err, "failed to reconcile DCGM")
					continue
				}
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
	}

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

type SysInfo interface {
	Arch() string
}

type sysInfo struct{}

func (*sysInfo) Arch() string {
	return runtime.GOARCH
}
