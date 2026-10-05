package probe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/probe"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
	"github.com/aws/eks-node-monitoring-agent/pkg/util"
)

// Runner executes one probe spec on its interval and emits conditions
// through the parent monitor's manager. It owns the probe's failure
// semantics: a startup grace period, a consecutive-failure threshold that
// resets on success, a consecutive-healthy recovery threshold that damps
// condition flapping, and a one-shot summary of failure episodes that
// recover before reaching the failure threshold. Each check keeps its own
// state and reports under its own reason and severity, and a failure is
// reported once per episode. Unknown results (checks that could not execute
// for monitoring-side reasons) never count toward either threshold.
type Runner struct {
	spec       probe.Spec
	transports map[probe.TransportKind]Transport
	manager    monitor.Manager
	log        logr.Logger
	// recoveryThreshold is spec.RecoveryThreshold with the zero value
	// defaulted to 1 (resolve on first healthy result).
	recoveryThreshold int

	// now and newTicker are injectable for tests.
	now       func() time.Time
	newTicker func(ctx context.Context, d time.Duration) <-chan time.Time

	startedAt time.Time
	liveness  *checkState
	// readiness is nil when the spec has no readiness check.
	readiness *checkState
}

// checkState is the failure state of one check. Each check counts its own
// results, so a failure in one check never adds to another check's streak.
type checkState struct {
	name     string
	check    probe.Check
	reason   string
	severity monitor.Severity

	consecutiveFailures int
	// consecutiveHealthy counts healthy results since the last unhealthy
	// one while a fired failure awaits resolution.
	consecutiveHealthy int
	// fired records that this episode's failure was reported. It is not
	// reported again until the check recovers.
	fired bool
	// streakStart is the cycle time of the current streak's first failure.
	streakStart time.Time
	// streakStartedInGrace records whether the current failure streak began
	// inside the startup grace period; such streaks are boot noise and do
	// not produce an episode summary.
	streakStartedInGrace bool
}

// NewRunner validates the spec and returns a Runner that emits through the
// given manager. A check's failure severity defaults to its reason's severity
// from reasons.yaml when the check does not set one; the recovery threshold
// defaults to 1 when the spec does not set one.
func NewRunner(spec probe.Spec, mgr monitor.Manager, log logr.Logger) (*Runner, error) {
	if err := Validate(spec); err != nil {
		return nil, err
	}
	recoveryThreshold := spec.RecoveryThreshold
	if recoveryThreshold == 0 {
		recoveryThreshold = 1
	}
	if spec.Checks.Diagnostics != nil {
		log.Info("probe diagnostics checks are not implemented yet and will be ignored", "subsystem", spec.Subsystem)
	}
	r := &Runner{
		spec:              spec,
		transports:        NewTransports(),
		manager:           mgr,
		log:               log.WithValues("probe", spec.Subsystem),
		recoveryThreshold: recoveryThreshold,
		now:               time.Now,
		newTicker:         util.TimeTickWithJitterContext,
		liveness:          newCheckState(livenessCheck, spec.Checks.Liveness),
	}
	if spec.Checks.Readiness != nil {
		r.readiness = newCheckState(readinessCheck, *spec.Checks.Readiness)
	}
	return r, nil
}

// newCheckState returns the initial state for a validated check.
func newCheckState(name string, check probe.Check) *checkState {
	severity := check.FailureSeverity
	if severity == "" {
		// Validate guarantees the reason is registered.
		meta, _ := reasons.ByName(check.ReasonOnFail)
		severity = meta.DefaultSeverity()
	}
	return &checkState{name: name, check: check, reason: check.ReasonOnFail, severity: severity}
}

// Start runs the probe loop until the context is canceled.
func (r *Runner) Start(ctx context.Context) error {
	r.startedAt = r.now()
	r.log.Info("starting probe", "interval", r.spec.Interval.Duration, "failureThreshold", r.spec.FailureThreshold, "startupGracePeriod", r.spec.StartupGracePeriod.Duration)
	ticks := r.newTicker(ctx, r.spec.Interval.Duration)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			if err := r.runOnce(ctx); err != nil {
				r.log.Error(err, "probe cycle failed to report")
			}
		}
	}
}

// runOnce executes one probe cycle. Liveness runs first, and readiness runs
// only when liveness is healthy: an agent that is not running cannot be asked
// whether it is doing its job, so readiness's state holds still meanwhile.
// The clock is read once, so every decision in a cycle uses the same time.
func (r *Runner) runOnce(ctx context.Context) error {
	now := r.now()
	liveness := r.do(ctx, r.liveness)
	err := r.apply(ctx, r.liveness, liveness, now)
	if liveness.Outcome != OutcomeHealthy || r.readiness == nil {
		return err
	}
	return errors.Join(err, r.apply(ctx, r.readiness, r.do(ctx, r.readiness), now))
}

// apply records one check result in the check's state and emits at most one
// notification for it.
func (r *Runner) apply(ctx context.Context, cs *checkState, result Result, now time.Time) error {
	switch result.Outcome {
	case OutcomeUnknown:
		// The check could not execute; this is not evidence about the agent
		// in either direction. Leave the failure and recovery counters
		// untouched.
		r.log.Info("probe check could not execute, skipping", "check", cs.name, "detail", result.Detail)
		return nil
	case OutcomeHealthy:
		return r.applyHealthy(ctx, cs, now)
	case OutcomeUnhealthy:
		return r.applyUnhealthy(ctx, cs, result, now)
	default:
		return fmt.Errorf("unexpected probe outcome %q", result.Outcome)
	}
}

func (r *Runner) applyHealthy(ctx context.Context, cs *checkState, now time.Time) error {
	streak := cs.consecutiveFailures
	cs.consecutiveFailures = 0
	if !cs.fired {
		if streak == 0 {
			return nil
		}
		// A failure streak ended below the failure threshold. The agent
		// cannot record its own brief outage, so emit one summary of the
		// completed episode, unless the streak began inside the startup
		// grace period, which is boot noise.
		if cs.streakStartedInGrace {
			r.log.Info("probe episode began during startup grace, suppressing summary", "check", cs.name, "failures", streak)
			return nil
		}
		// Measured rather than derived from the interval: an Unknown round
		// inside a streak takes time without counting as a failure.
		elapsed := now.Sub(cs.streakStart).Round(time.Second)
		r.log.Info("probe episode recovered below threshold, emitting summary", "check", cs.name, "failures", streak, "elapsed", elapsed)
		return r.manager.Notify(ctx, monitor.Condition{
			Reason:   cs.reason,
			Message:  fmt.Sprintf("%s %s check failed %s and recovered %s after the first failure", r.spec.Subsystem, cs.name, timesText(streak), elapsed),
			Severity: summarySeverity(cs.severity),
		})
	}
	cs.consecutiveHealthy++
	if cs.consecutiveHealthy < r.recoveryThreshold {
		r.log.Info("probe healthy below recovery threshold, awaiting hysteresis", "check", cs.name, "consecutiveHealthy", cs.consecutiveHealthy, "recoveryThreshold", r.recoveryThreshold)
		return nil
	}
	cs.fired = false
	cs.consecutiveHealthy = 0
	if cs.severity != monitor.SeverityFatal {
		// Only a Fatal check sets a condition. A Warning or Info check
		// recorded an event when it fired, so there is nothing to resolve.
		r.log.Info("probe recovered", "check", cs.name, "reason", cs.reason)
		return nil
	}
	r.log.Info("probe recovered, resolving condition", "check", cs.name, "reason", cs.reason)
	return r.manager.Notify(ctx, monitor.Condition{
		Reason:   cs.reason,
		Message:  fmt.Sprintf("The %s %s check is healthy again", r.spec.Subsystem, cs.name),
		Severity: monitor.SeverityFatal,
		Resolved: true,
	})
}

func (r *Runner) applyUnhealthy(ctx context.Context, cs *checkState, result Result, now time.Time) error {
	inGrace := now.Sub(r.startedAt) < r.spec.StartupGracePeriod.Duration
	if cs.consecutiveFailures == 0 {
		cs.streakStart = now
		cs.streakStartedInGrace = inGrace
	}
	cs.consecutiveFailures++
	// A failure interrupts recovery: resolution requires consecutive
	// healthy results.
	cs.consecutiveHealthy = 0
	switch {
	case cs.fired:
		// Already reported in this episode. Re-sending would patch the node
		// (Fatal) or record an event (Warning, Info) every round; the
		// exporter's heartbeat keeps a Fatal condition's timestamp current.
		r.log.Info("probe still failing, already reported", "check", cs.name, "detail", result.Detail, "consecutiveFailures", cs.consecutiveFailures)
		return nil
	case inGrace:
		r.log.Info("probe failing within startup grace period, suppressing", "check", cs.name, "detail", result.Detail, "consecutiveFailures", cs.consecutiveFailures)
		return nil
	case cs.consecutiveFailures < r.spec.FailureThreshold:
		r.log.Info("probe failing below threshold", "check", cs.name, "detail", result.Detail, "consecutiveFailures", cs.consecutiveFailures)
		return nil
	}
	cs.fired = true
	return r.manager.Notify(ctx, monitor.Condition{
		Reason:   cs.reason,
		Message:  result.Detail,
		Severity: cs.severity,
	})
}

// do executes a single check via its transport and prefixes the detail with
// the probe and check identity for condition messages.
func (r *Runner) do(ctx context.Context, cs *checkState) Result {
	transport, ok := r.transports[cs.check.Transport]
	if !ok {
		// Validate rejects unknown transports; this guards runtime drift.
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("no transport %q", cs.check.Transport)}
	}
	result := transport.Do(ctx, cs.check)
	if result.Detail != "" {
		result.Detail = fmt.Sprintf("%s %s check: %s", r.spec.Subsystem, cs.name, result.Detail)
	}
	return result
}

// summarySeverity is the severity of the summary for a failure streak that
// recovered below the threshold: a Warning, or Info for an Info check, so the
// summary is never louder than the check's own failure.
func summarySeverity(checkSeverity monitor.Severity) monitor.Severity {
	if checkSeverity == monitor.SeverityInfo {
		return monitor.SeverityInfo
	}
	return monitor.SeverityWarning
}

// timesText renders a failure count for a message: "1 time", "2 times".
func timesText(n int) string {
	if n == 1 {
		return "1 time"
	}
	return fmt.Sprintf("%d times", n)
}
