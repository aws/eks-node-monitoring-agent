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
// resets on success, and a recovery signal once a previously failing probe
// is healthy again. Unknown results (checks that could not execute for
// monitoring-side reasons) never count toward the threshold.
type Runner struct {
	spec       probe.Spec
	transports map[probe.TransportKind]Transport
	manager    monitor.Manager
	log        logr.Logger
	severity   monitor.Severity

	// now and newTicker are injectable for tests.
	now       func() time.Time
	newTicker func(ctx context.Context, d time.Duration) <-chan time.Time

	startedAt           time.Time
	consecutiveFailures int
	failureEmitted      bool
}

// NewRunner validates the spec and returns a Runner that emits through the
// given manager. The failure severity defaults to the reason's severity from
// reasons.yaml when the spec does not set one.
func NewRunner(spec probe.Spec, mgr monitor.Manager, log logr.Logger) (*Runner, error) {
	if err := Validate(spec); err != nil {
		return nil, err
	}
	meta, _ := reasons.ByName(spec.ReasonOnFail)
	severity := spec.FailureSeverity
	if severity == "" {
		severity = meta.DefaultSeverity()
	}
	if spec.Checks.Diagnostics != nil {
		log.Info("probe diagnostics checks are not implemented yet and will be ignored", "subsystem", spec.Subsystem)
	}
	return &Runner{
		spec:       spec,
		transports: NewTransports(),
		manager:    mgr,
		log:        log.WithValues("probe", spec.Subsystem),
		severity:   severity,
		now:        time.Now,
		newTicker:  util.TimeTickWithJitterContext,
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
	result := r.check(ctx)
	switch result.Outcome {
	case OutcomeUnknown:
		// The check could not execute; this is not evidence about the agent.
		// Leave the failure counter untouched.
		r.log.Info("probe check could not execute, skipping cycle", "detail", result.Detail)
		return nil
	case OutcomeHealthy:
		r.consecutiveFailures = 0
		if !r.failureEmitted {
			return nil
		}
		r.failureEmitted = false
		r.log.Info("probe recovered, resolving condition", "reason", r.spec.ReasonOnFail)
		return r.manager.Notify(ctx, monitor.Condition{
			Reason:   r.spec.ReasonOnFail,
			Message:  fmt.Sprintf("The %s probe is healthy again", r.spec.Subsystem),
			Severity: r.severity,
			Resolved: true,
		})
	case OutcomeUnhealthy:
		r.consecutiveFailures++
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

// check runs the probe's checks in order and combines their results:
// any unhealthy result wins, then any unknown, then healthy.
func (r *Runner) check(ctx context.Context) Result {
	liveness := r.do(ctx, "liveness", r.spec.Checks.Liveness)
	if liveness.Outcome != OutcomeHealthy {
		return liveness
	}
	if r.spec.Checks.Readiness != nil {
		return r.do(ctx, "readiness", *r.spec.Checks.Readiness)
	}
	return liveness
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
