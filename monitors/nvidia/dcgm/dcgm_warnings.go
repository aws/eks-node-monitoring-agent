//go:build !darwin

package dcgm

import (
	"slices"
	"strings"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

// warningReNotifyInterval keeps an unchanged Warning's Event from aging out
// under the apiserver's default 1h event TTL, matching the networking
// Corefile detector.
const warningReNotifyInterval = 30 * time.Minute

// suppressRepeatedWarnings applies the re-notify rule to the Warnings in conds
// that selected picks: they are kept, once each (DCGM reports the same one for
// every GPU), when that set of Warnings changes or warningReNotifyInterval has
// passed since it was last kept, and dropped otherwise. Other conditions are
// returned unchanged and in order.
func suppressRepeatedWarnings(notified *renotify.Tracker[string], now time.Time, conds []monitor.Condition, selected func(monitor.Condition) bool) []monitor.Condition {
	// Only Warnings are rate-limited: a Fatal condition is node state that the
	// exporter keeps current, not an event, so it always passes through.
	isSelected := func(c monitor.Condition) bool {
		return c.Severity == monitor.SeverityWarning && selected(c)
	}

	// The tracked state is the set of selected Warnings in this call, so build
	// one key from their reason and message.
	var keys []string
	for _, c := range conds {
		if isSelected(c) {
			keys = append(keys, c.Reason+"\x00"+c.Message)
		}
	}

	// None present: the state is healthy, so the next Warning is reported
	// right away (the Corefile detector does the same on a healthy probe).
	if len(keys) == 0 {
		notified.Reset()
		return conds
	}

	// Sort so the key does not depend on GPU order, and drop duplicates so the
	// same Warning on several GPUs counts once.
	slices.Sort(keys)
	key := strings.Join(slices.Compact(keys), "\n")

	// Report the set if it changed or warningReNotifyInterval has passed, and
	// record it. Unlike the Corefile detector, nothing here learns whether the
	// event was sent (the caller notifies the manager later), so it is
	// recorded now.
	keep := notified.ShouldNotify(key, now)
	if keep {
		notified.Notified(key, now)
	}

	// Rebuild the list in its original order: other conditions always, and
	// each selected Warning once if the set is being reported, otherwise not
	// at all.
	var out []monitor.Condition
	kept := map[monitor.Condition]bool{}
	for _, c := range conds {
		if isSelected(c) {
			if !keep || kept[c] {
				continue
			}
			kept[c] = true
		}
		out = append(out, c)
	}
	return out
}
