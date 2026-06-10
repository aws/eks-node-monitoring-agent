// Package npa implements health detection for the Network Policy Agent (NPA).
//
// It is NOT a standalone monitor. The networking monitor constructs a Detector
// and drives its handlers, so all NPA conditions are emitted through the
// networking monitor's manager and therefore surface under the NetworkingReady
// node condition. NPA detection runs on Auto Mode nodes only.
//
// Severity policy (mirrors IPAMD):
//   - NPANotRunning        Fatal   (persistently down; systemd can't recover it)
//   - NPARepeatedlyRestart Warning (crash loop; root cause often not node-local)
//   - NPABPFRecoveryError  Warning (startup BPF recovery error; self-heals via reconcile)
package npa

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/go-logr/logr"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

const (
	// Unit is the systemd unit name for the Network Policy Agent.
	Unit = "aws-network-policy-agent.service"

	// repeatedRestartThreshold is the systemd NRestarts delta within a single
	// poll interval that indicates a crash loop.
	repeatedRestartThreshold uint32 = 5

	// consistencyWindow is how long NPA must be continuously not-running before
	// the NPANotRunning condition is emitted.
	consistencyWindow = 15 * time.Minute
)

// Detector holds the state and logic for NPA health checks. Create one with New
// and drive it from the networking monitor's Register (D-Bus state polling +
// log subscription).
type Detector struct {
	manager           monitor.Manager
	log               logr.Logger
	npaNotRunningTime time.Time // the last time NPA was first observed not running
	previousRestarts  uint32    // last observed systemd NRestarts baseline
}

// New returns a Detector that emits conditions through the given manager. Pass
// the networking monitor's manager so conditions surface under NetworkingReady.
func New(manager monitor.Manager, log logr.Logger) *Detector {
	return &Detector{manager: manager, log: log}
}

// stringProp reads a string-valued systemd unit property via D-Bus, returning
// "" if it can't be read. Used for best-effort diagnostic enrichment.
func stringProp(dc *dbus.Conn, prop string) string {
	p, err := dc.GetUnitPropertyContext(context.Background(), Unit, prop)
	if err != nil {
		return ""
	}
	s, _ := p.Value.Value().(string)
	return s
}

// HandleState polls systemd via D-Bus for NPA crash-loop (NRestarts delta) and
// not-running (ActiveState) signals. SubState/Result are read best-effort and
// folded into the condition message for diagnostics. Called on a fixed interval.
func (d *Detector) HandleState() error {
	dc, err := dbus.NewWithContext(context.Background())
	if err != nil {
		d.log.Error(err, "failed to connect to D-Bus")
		return nil
	}
	defer dc.Close()

	// ActiveState drives not-running detection. If we can't read it, skip this
	// cycle rather than assuming NPA is down.
	property, err := dc.GetUnitPropertyContext(context.Background(), Unit, "ActiveState")
	if err != nil {
		d.log.Error(err, "failed to get ActiveState for NPA")
		return nil
	}
	active := property.Value.Value().(string) == "active"

	// Best-effort diagnostics: why/how the unit is in its current state.
	subState := stringProp(dc, "SubState") // e.g. running / dead / failed / auto-restart
	result := stringProp(dc, "Result")     // e.g. success / exit-code / signal / oom-kill / watchdog

	// --- NPANotRunning ---
	if err := d.checkNotRunning(active, subState, result); err != nil {
		return err
	}
	// If NPA is not running, skip the crash-loop check (no point reading NRestarts).
	if !active {
		return nil
	}

	// --- NPARepeatedlyRestart ---
	properties, err := dc.GetAllPropertiesContext(context.Background(), Unit)
	if err != nil {
		d.log.Error(err, "failed to get NPA unit properties")
		return nil
	}
	nRestarts, ok := properties["NRestarts"]
	if !ok {
		return nil
	}
	currentRestarts, ok := nRestarts.(uint32)
	if !ok {
		return nil
	}
	return d.checkRepeatedlyRestart(currentRestarts, subState, result)
}

// checkNotRunning applies the consistency window logic for the NPANotRunning
// condition. The first time NPA is observed not running, the detection time is
// recorded but no condition is emitted. Only once NPA has been continuously not
// running for at least consistencyWindow is NPANotRunning emitted (Fatal).
// Observing NPA active resets the window.
func (d *Detector) checkNotRunning(active bool, subState, result string) error {
	if active {
		d.npaNotRunningTime = time.Time{} // reset — NPA is running
		return nil
	}

	now := time.Now()
	if d.npaNotRunningTime.IsZero() {
		d.npaNotRunningTime = now
		return nil
	}
	if now.Sub(d.npaNotRunningTime) >= consistencyWindow {
		return d.manager.Notify(context.Background(),
			reasons.NPANotRunning.
				Builder().
				Message(fmt.Sprintf("NPA is not running on this Auto Mode node (SubState=%q, Result=%q)", subState, result)).
				Build(),
		)
	}
	return nil
}

// checkRepeatedlyRestart computes the delta of systemd restart counts since the
// last poll and emits NPARepeatedlyRestart (Warning) if the delta meets the
// threshold. The current restart count is always recorded as the new baseline.
func (d *Detector) checkRepeatedlyRestart(currentRestarts uint32, subState, result string) error {
	delta := currentRestarts - d.previousRestarts
	d.previousRestarts = currentRestarts

	if delta >= repeatedRestartThreshold {
		return d.manager.Notify(context.Background(),
			reasons.NPARepeatedlyRestart.
				Builder().
				Message(fmt.Sprintf("NPA restarted %d times in last poll interval (threshold: %d, SubState=%q, Result=%q)",
					delta, repeatedRestartThreshold, subState, result)).
				Build(),
		)
	}
	return nil
}

// HandleLogs pattern-matches NPA error log lines from the startup BPF recovery
// flow. Both recovery-level and map-level failures map to the single
// NPABPFRecoveryError condition (Warning) since they originate from the same
// best-effort recovery path and share the same remediation.
func (d *Detector) HandleLogs(line string) error {
	// Recovery failures (bpf_client.go lines 184, 416)
	if strings.Contains(line, "Failed to recover the BPF state") ||
		strings.Contains(line, "BPF State Recovery failed error") {
		return d.manager.Notify(context.TODO(),
			reasons.NPABPFRecoveryError.
				Builder().
				Message("NPA BPF state recovery failed after restart").
				MinOccurrences(2).
				Build(),
		)
	}

	// eBPF map errors (bpf_client.go lines 390, 439, 471)
	if strings.Contains(line, "failed to recover global maps") ||
		strings.Contains(line, "got err for ingress in-mem map") ||
		strings.Contains(line, "got err for egress in-mem map") ||
		strings.Contains(line, "got err for cluster policy ingress in-mem map") ||
		strings.Contains(line, "got err for cluster policy egress in-mem map") {
		return d.manager.Notify(context.TODO(),
			reasons.NPABPFRecoveryError.
				Builder().
				Message("NPA eBPF map recovery/write error detected").
				MinOccurrences(2).
				Build(),
		)
	}

	return nil
}
