//go:build !darwin

package dcgm

import (
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
)

func NewDCGMSystem(dcgmClient DCGM, diagType dcgmapi.DiagType) *DCGMSystem {
	return &DCGMSystem{
		dcgm:                    dcgmClient,
		diagType:                diagType,
		instanceTypeInfoProvider: instanceinfo.NewInstanceTypeInfoProvider(),
		// TODO: consider exposing this parameter.
		fieldValueWindow: 5 * time.Minute,
	}
}

// NewDCGMSystemWithInstanceTypeInfoProvider creates a DCGMSystem with a custom
// InstanceTypeInfoProvider, primarily for testing.
func NewDCGMSystemWithInstanceTypeInfoProvider(dcgmClient DCGM, diagType dcgmapi.DiagType, provider instanceinfo.InstanceTypeInfoProvider) *DCGMSystem {
	return &DCGMSystem{
		dcgm:                    dcgmClient,
		diagType:                diagType,
		instanceTypeInfoProvider: provider,
		fieldValueWindow:         5 * time.Minute,
	}
}

type DCGMSystem struct {
	dcgm                    DCGM
	diagType                dcgmapi.DiagType
	instanceTypeInfoProvider instanceinfo.InstanceTypeInfoProvider

	// fieldValueWindow is the time window used to fetch changes in field
	// identifiers watched by dcgm.
	fieldValueWindow time.Duration
}
