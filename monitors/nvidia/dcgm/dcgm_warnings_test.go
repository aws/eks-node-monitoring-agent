//go:build !darwin

package dcgm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

func TestSuppressRepeatedWarnings(t *testing.T) {
	selectedWarning := monitor.Condition{Reason: "Selected", Message: "m", Severity: monitor.SeverityWarning}
	selectedFatal := monitor.Condition{Reason: "Selected", Message: "m", Severity: monitor.SeverityFatal}
	otherWarning := monitor.Condition{Reason: "Other", Message: "m", Severity: monitor.SeverityWarning}
	isSelected := func(c monitor.Condition) bool { return c.Reason == "Selected" }

	notified := renotify.New[string](warningReNotifyInterval)
	now := time.Unix(0, 0)
	call := func(conds ...monitor.Condition) []monitor.Condition {
		return suppressRepeatedWarnings(notified, now, conds, isSelected)
	}

	// First report is kept once, even when repeated in the same call (one per
	// GPU); other conditions keep their order.
	assert.Equal(t, []monitor.Condition{selectedWarning, selectedFatal},
		call(selectedWarning, selectedFatal, selectedWarning))

	// Unchanged on the next call: dropped. Fatal conditions and Warnings that
	// are not selected always pass.
	now = now.Add(5 * time.Minute)
	assert.Equal(t, []monitor.Condition{selectedFatal, otherWarning},
		call(selectedWarning, selectedFatal, otherWarning))

	// A changed set of Warnings is kept right away.
	changed := selectedWarning
	changed.Message = "changed"
	assert.Equal(t, []monitor.Condition{selectedWarning, changed}, call(selectedWarning, changed))

	// The same set in a different order (DCGM does not promise an order across
	// calls) is unchanged: dropped.
	assert.Empty(t, call(changed, selectedWarning))

	// Unchanged again, then kept once warningReNotifyInterval has passed.
	now = now.Add(warningReNotifyInterval - time.Minute)
	assert.Empty(t, call(selectedWarning, changed))
	now = now.Add(time.Minute)
	assert.Equal(t, []monitor.Condition{selectedWarning, changed}, call(selectedWarning, changed))

	// Cleared, then back: kept right away.
	now = now.Add(time.Minute)
	assert.Empty(t, call())
	now = now.Add(time.Minute)
	assert.Equal(t, []monitor.Condition{selectedWarning}, call(selectedWarning))
}
