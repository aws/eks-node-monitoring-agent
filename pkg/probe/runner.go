package probe

import (
	"context"
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
// condition flapping, and a one-shot Warning summarizing failure episodes
// that recover before reaching the failure threshold. Unknown results
// (checks that could not execute for monitoring-side reasons) never count
// toward either threshold.
type Runner struct {
	spec       probe.Spec
	transports map[probe.TransportKind]Transport
	manager    monitor.Manager
	log        logr.Logger
	severity   monitor.Severity
	// recoveryThreshold is spec.RecoveryThreshold with the zero value
	// defaulted to 1 (resolve on first healthy result).
	recoveryThreshold int

	// now and newTicker are injectable for tests.
	now       func() time.Time
	newTicker func(ctx context.Context, d time.Duration) <-chan time.Time

	startedAt           time.Time
	consecutiveFailures int
	failureEmitted      bool
	// consecutiveHealthy counts healthy results since the last unhealthy
	// one while a fired failure awaits resolution.
	consecutiveHealthy int
	// lastFailedCheck names the check ("liveness" or "readiness") that
	// produced the streak's most recent unhealthy result, for the episode
	// summary message.
	lastFailedCheck string
	// streakStartedInGrace records whether the current failure streak began
	// inside the startup grace period; such streaks are boot noise and do
	// not produce an episode summary.
	streakStartedInGrace bool
}

// NewRunner validates the spec and returns a Runner that emits through the
// given manager. The failure severity defaults to the reason's severity from
// reasons.yaml when the spec does not set one; the recovery threshold
// defaults to 1 when the spec does not set one.
func NewRunner(spec probe.Spec, mgr monitor.Manager, log logr.Logger) (*Runner, error) {
	if err := Validate(spec); err != nil {
		return nil, err
	}
	meta, _ := reasons.ByName(spec.ReasonOnFail)
	severity := spec.FailureSeverity
	if severity == "" {
		severity = meta.DefaultSeverity()
	}
	recoveryThreshold := spec.RecoveryThreshold
	if recoveryThreshold == 0 {
		recoveryThreshold = 1
	}
	if spec.Checks.Diagnostics != nil {
		log.Info("probe diagnostics checks are not implemented yet and will be ignored", "subsystem", spec.Subsystem)
	}
	return &Runner{
		spec:              spec,
		transports:        NewTransports(),
		manager:           mgr,
		log:               log.WithValues("probe", spec.Subsystem),
		severity:          severity,
		recoveryThreshold: recoveryThreshold,
		now:               time.Now,
		newTicker:         util.TimeTickWithJitterContext,
	}, nil
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

// runOnce executes one probe cycle: liveness, then readiness when configured,
// then applies the failure semantics to the combined result.
func (r *Runner) runOnce(ctx context.Context) error {
	result, checkName := r.check(ctx)
	switch result.Outcome {
	case OutcomeUnknown:
		// The check could not execute; this is not evidence about the agent
		// in either direction. Leave the failure and recovery counters
		// untouched.
		r.log.Info("probe check could not execute, skipping cycle", "detail", result.Detail)
		return nil
	case OutcomeHealthy:
		streak := r.consecutiveFailures
		r.consecutiveFailures = 0
		if !r.failureEmitted {
			if streak == 0 {
				return nil
			}
			// A failure streak completed below the failure threshold. The
			// agent cannot record its own brief outage, so emit exactly one
			// Warning summarizing the completed episode — unless the streak
			// began inside the startup grace period, which is boot noise.
			startedInGrace := r.streakStartedInGrace
			r.streakStartedInGrace = false
			if startedInGrace {
				r.log.Info("probe episode began during startup grace, suppressing summary", "failures", streak)
				return nil
			}
			duration := time.Duration(streak) * r.spec.Interval.Duration
			r.log.Info("probe episode recovered below threshold, emitting summary", "failures", streak, "duration", duration)
			return r.manager.Notify(ctx, monitor.Condition{
				Reason:   r.spec.ReasonOnFail,
				Message:  fmt.Sprintf("%s %s check unhealthy for %d consecutive checks over %s, recovered", r.spec.Subsystem, r.lastFailedCheck, streak, duration),
				Severity: monitor.SeverityWarning,
			})
		}
		r.consecutiveHealthy++
		if r.consecutiveHealthy < r.recoveryThreshold {
			r.log.Info("probe healthy below recovery threshold, awaiting hysteresis", "consecutiveHealthy", r.consecutiveHealthy, "recoveryThreshold", r.recoveryThreshold)
			return nil
		}
		r.failureEmitted = false
		r.consecutiveHealthy = 0
		r.streakStartedInGrace = false
		r.log.Info("probe recovered, resolving condition", "reason", r.spec.ReasonOnFail)
		return r.manager.Notify(ctx, monitor.Condition{
			Reason:   r.spec.ReasonOnFail,
			Message:  fmt.Sprintf("The %s probe is healthy again", r.spec.Subsystem),
			Severity: r.severity,
			Resolved: true,
		})
	case OutcomeUnhealthy:
		if r.consecutiveFailures == 0 {
			r.streakStartedInGrace = r.now().Sub(r.startedAt) < r.spec.StartupGracePeriod.Duration
		}
		r.consecutiveFailures++
		// A failure interrupts recovery: the resolution hysteresis requires
		// consecutive healthy results.
		r.consecutiveHealthy = 0
		r.lastFailedCheck = checkName
		if grace := r.spec.StartupGracePeriod.Duration; r.now().Sub(r.startedAt) < grace {
			r.log.Info("probe failing within startup grace period, suppressing", "detail", result.Detail, "consecutiveFailures", r.consecutiveFailures)
			return nil
		}
		if r.consecutiveFailures < r.spec.FailureThreshold {
			r.log.Info("probe failing below threshold", "detail", result.Detail, "consecutiveFailures", r.consecutiveFailures)
			return nil
		}
		r.failureEmitted = true
		return r.manager.Notify(ctx, monitor.Condition{
			Reason:   r.spec.ReasonOnFail,
			Message:  result.Detail,
			Severity: r.severity,
		})
	default:
		return fmt.Errorf("unexpected probe outcome %q", result.Outcome)
	}
}

// check runs the probe's checks in order and combines their results: any
// unhealthy result wins, then any unknown, then healthy. The second return
// value names the check that produced the result.
func (r *Runner) check(ctx context.Context) (Result, string) {
	liveness := r.do(ctx, "liveness", r.spec.Checks.Liveness)
	if liveness.Outcome != OutcomeHealthy {
		return liveness, "liveness"
	}
	if r.spec.Checks.Readiness != nil {
		return r.do(ctx, "readiness", *r.spec.Checks.Readiness), "readiness"
	}
	return liveness, "liveness"
}

// do executes a single check via its transport and prefixes the detail with
// the probe and check identity for condition messages.
func (r *Runner) do(ctx context.Context, name string, check probe.Check) Result {
	transport, ok := r.transports[check.Transport]
	if !ok {
		// Validate rejects unknown transports; this guards runtime drift.
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("no transport %q", check.Transport)}
	}
	result := transport.Do(ctx, check)
	if result.Detail != "" {
		result.Detail = fmt.Sprintf("%s %s check: %s", r.spec.Subsystem, name, result.Detail)
	}
	return result
}
