//go:build !darwin

package dcgm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// TODO: this will not work unless the DCGM image containing the nv-hostengine
// also contains the correct binaries for NVVS (validation suite) as well.
func (s *DCGMSystem) ActiveDiagnostic(ctx context.Context) ([]monitor.Condition, error) {
	logger := log.FromContext(ctx)

	if s.diagType <= 0 {
		return nil, nil
	}

	logger.V(2).Info("starting DCGM active diagnostics", "level", s.diagType)
	diagResults, err := s.dcgm.RunDiag(dcgmapi.DiagType(s.diagType))
	if err != nil {
		if errors.Is(err, ErrNotInitialized) {
			logger.V(2).Info("could not run active diagnostics. DCGM is not yet initialized")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to run DCGM diagnostics: %w", err)
	}

	var conditions []monitor.Condition

	logger.V(4).Info("completed DCGM active diagnostics", "results", diagResults)
	for _, swResult := range diagResults.Software {
		// see: https://github.com/NVIDIA/go-dcgm/blob/850266c9c8a58cb377b7ad25eed1f7114a6d5434/pkg/dcgm/diag.go#L35-L49
		if swResult.Status == "fail" {
			conditions = append(conditions,
				reasons.DCGMDiagnosticFailure.
					Builder().
					Message(fmt.Sprintf("DCGM Diagnostic failed for test %s with error: %s", swResult.TestName, swResult.ErrorMessage)).
					Build(),
			)
		}
	}

	return conditions, nil
}

func GetDiagType() dcgmapi.DiagType {
	logger := log.FromContext(context.TODO())

	// TODO: this may be changed in the future if this is a default desired
	// detection that does not disturb workloads.
	diagnosticLevel := 0
	if diagLevelStr, ok := os.LookupEnv("DCGM_DIAG_LEVEL"); ok {
		var err error
		if diagnosticLevel, err = strconv.Atoi(diagLevelStr); err != nil {
			logger.Info("failed to parse DCGM diagnostic level", "raw", diagLevelStr, "error", err)
		}
	}
	if diagnosticLevel <= 0 {
		logger.V(5).Info("DCGM active diagnostics will be skipped due to a lack of non-zero diagnostic level")
	}
	return dcgmapi.DiagType(diagnosticLevel)
}
