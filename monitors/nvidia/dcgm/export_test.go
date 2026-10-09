//go:build !darwin

package dcgm

import "time"

// SetClock replaces the clock DCGMSystem uses to rate-limit repeated Warnings,
// so tests in package dcgm_test can step through warningReNotifyInterval.
func SetClock(s *DCGMSystem, now func() time.Time) { s.now = now }

// WarningReNotifyInterval exposes warningReNotifyInterval to package dcgm_test.
const WarningReNotifyInterval = warningReNotifyInterval
