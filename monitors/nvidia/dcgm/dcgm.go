//go:build !darwin

package dcgm

import (
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

func NewDCGMSystem(dcgmClient DCGM, diagType dcgmapi.DiagType) *DCGMSystem {
	return &DCGMSystem{
		dcgm:     dcgmClient,
		diagType: diagType,
		// TODO: consider exposing this parameter.
		fieldValueWindow: 5 * time.Minute,
	}
}

type DCGMSystem struct {
	dcgm     DCGM
	diagType dcgmapi.DiagType

	// fieldValueWindow is the time window used to fetch changes in field
	// identifiers watched by dcgm.
	fieldValueWindow time.Duration
}
