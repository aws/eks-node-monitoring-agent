//go:build !darwin

package dcgm

import (
	"context"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/nvidiasmi"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

func NewDCGMSystem(dcgmClient DCGM, diagType dcgmapi.DiagType) *DCGMSystem {
	return NewDCGMSystemWithInstanceTypeInfoProvider(dcgmClient, diagType, instanceinfo.NewInstanceTypeInfoProvider())
}

// NewDCGMSystemWithInstanceTypeInfoProvider creates a DCGMSystem with a custom
// InstanceTypeInfoProvider, primarily for testing.
func NewDCGMSystemWithInstanceTypeInfoProvider(dcgmClient DCGM, diagType dcgmapi.DiagType, provider instanceinfo.InstanceTypeInfoProvider) *DCGMSystem {
	return &DCGMSystem{
		dcgm:                     dcgmClient,
		diagType:                 diagType,
		instanceTypeInfoProvider: provider,
		fieldValueWindow:         5 * time.Minute,
		fieldWarnings:            renotify.New[string](classify.WarningReNotifyInterval),
		healthWarnings:           renotify.New[string](classify.WarningReNotifyInterval),
		now:                      time.Now,
		fabricCrossCheck:         nvidiasmi.NewReader().Read,
	}
}

type DCGMSystem struct {
	dcgm                     DCGM
	diagType                 dcgmapi.DiagType
	instanceTypeInfoProvider instanceinfo.InstanceTypeInfoProvider

	// fieldValueWindow is the time window used to fetch changes in field
	// identifiers watched by dcgm.
	fieldValueWindow time.Duration

	// fieldWarnings and healthWarnings track the Warnings that WatchFields and
	// HealthCheck report on every call for an unchanged Fabric Manager status,
	// so repeats are not reported (see classify.SuppressRepeatedWarnings).
	fieldWarnings  *renotify.Tracker[string]
	healthWarnings *renotify.Tracker[string]

	// now is the clock for fieldWarnings and healthWarnings; tests replace it.
	now func() time.Time

	// fabricCrossCheck reads nvidia-smi for a faulting mask; tests replace it.
	fabricCrossCheck func(context.Context) (*classify.FabricCrossCheck, error)
}
