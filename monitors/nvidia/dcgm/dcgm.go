//go:build !darwin

package dcgm

import (
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

func NewDCGMSystem(dcgmClient DCGM, diagType dcgmapi.DiagType) *DCGMSystem {
	return &DCGMSystem{
		dcgm:                    dcgmClient,
		diagType:                diagType,
		expectedGPUCountProvider: NewEC2ExpectedGPUCountProvider(),
		// TODO: consider exposing this parameter.
		fieldValueWindow: 5 * time.Minute,
	}
}

// NewDCGMSystemWithExpectedGPUCountProvider creates a DCGMSystem with a custom
// ExpectedGPUCountProvider, primarily for testing.
func NewDCGMSystemWithExpectedGPUCountProvider(dcgmClient DCGM, diagType dcgmapi.DiagType, provider ExpectedGPUCountProvider) *DCGMSystem {
	return &DCGMSystem{
		dcgm:                    dcgmClient,
		diagType:                diagType,
		expectedGPUCountProvider: provider,
		fieldValueWindow:         5 * time.Minute,
	}
}

type DCGMSystem struct {
	dcgm                    DCGM
	diagType                dcgmapi.DiagType
	expectedGPUCountProvider ExpectedGPUCountProvider

	// fieldValueWindow is the time window used to fetch changes in field
	// identifiers watched by dcgm.
	fieldValueWindow time.Duration
}
