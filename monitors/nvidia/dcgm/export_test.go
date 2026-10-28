//go:build !darwin

package dcgm

import (
	"time"

	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
)

// SetClock replaces the clock DCGMSystem uses to rate-limit repeated Warnings,
// so tests in package dcgm_test can step through WarningReNotifyInterval.
func SetClock(s *DCGMSystem, now func() time.Time) { s.now = now }

// WarningReNotifyInterval re-exports classify.WarningReNotifyInterval to package dcgm_test.
const WarningReNotifyInterval = classify.WarningReNotifyInterval
