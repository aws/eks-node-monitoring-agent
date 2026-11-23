// Package controller runs the NMA soak test's release state machine. A
// release pipeline writes a release request to SSM, the controller waits for
// that NMA build to roll out on the soak cluster, evaluates a window of agent
// samples for leaks, and writes the verdict back to SSM for the pipeline to
// check. Between releases it keeps evaluating rolling windows so a leak or a
// broken soak can still be alarmed on.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/analysis"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/churn"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
)

// Request is the release a pipeline asks the soak to
// evaluate.
type Request struct {
	ReleaseID      string `json:"release_id"`
	ReleaseVersion string `json:"release_version,omitempty"`
	// ImageURI is the agent image the release ships. When set, the window
	// only starts once every agent pod runs it.
	ImageURI     string `json:"image_uri,omitempty"`
	ChartVersion string `json:"chart_version,omitempty"`
}

// Status is where a release is in the soak.
type Status string

const (
	StatusRollingOut     Status = "ROLLING_OUT"
	StatusEvaluating     Status = "EVALUATING"
	StatusComplete       Status = "COMPLETE"
	StatusRolloutTimeout Status = "ROLLOUT_TIMEOUT"
	// StatusSuperseded means a newer release arrived before this one's
	// window finished.
	StatusSuperseded Status = "SUPERSEDED"
)

// ReleaseResult is what the controller writes for the pipeline to check.
// A release passes only when Status is COMPLETE and Verdict is PASS or
// RECORD_ONLY.
type ReleaseResult struct {
	Request
	Status           Status           `json:"status"`
	Detail           string           `json:"detail,omitempty"`
	RolloutStartedAt time.Time        `json:"rollout_started_at"`
	WindowStart      *time.Time       `json:"window_start,omitempty"`
	WindowEnd        *time.Time       `json:"window_end,omitempty"`
	Verdict          analysis.Verdict `json:"verdict,omitempty"`
	Analysis         *analysis.Result `json:"analysis,omitempty"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// Passed reports whether the result lets the release through.
func (r ReleaseResult) Passed() bool {
	return r.Status == StatusComplete && (r.Verdict == analysis.VerdictPass || r.Verdict == analysis.VerdictRecordOnly)
}

// maxStoredFindings keeps the result under the SSM parameter size limit.
const maxStoredFindings = 20

// ParameterStore reads and writes the request and result parameters.
type ParameterStore interface {
	// Get returns the parameter value, and false if it doesn't exist.
	Get(ctx context.Context, name string) (string, bool, error)
	Put(ctx context.Context, name, value string) error
}

// Sampler collects agent samples.
type Sampler interface {
	Collect(ctx context.Context) ([]collector.Sample, error)
}

// Metrics publishes soak metrics.
type Metrics interface {
	PublishSamples(ctx context.Context, samples []collector.Sample) error
	PublishChurn(ctx context.Context, stats churn.Stats, now time.Time) error
	PublishResult(ctx context.Context, result analysis.Result, now time.Time) error
	PublishHeartbeat(ctx context.Context, now time.Time) error
}

// RolloutChecker reports whether the agent has finished rolling out.
type RolloutChecker interface {
	// RolledOut reports whether every agent pod is available and, when
	// imageURI is set, running it. detail explains why not.
	RolledOut(ctx context.Context, imageURI string) (done bool, detail string, err error)
}

// ChurnStats exposes the churner's counters.
type ChurnStats interface {
	Stats() churn.Stats
}

// Config configures the controller.
type Config struct {
	RequestParameter string
	ResultParameter  string
	// Window is how long a release is evaluated, from rollout complete. It
	// includes the analysis warm-up and must end before the pipeline checks
	// the result.
	Window         time.Duration
	RolloutTimeout time.Duration
	Thresholds     analysis.Thresholds
}

// Default timing. A 5h window plus up to 30m of rollout fits in a 6h
// pipeline wait.
const (
	DefaultWindow          = 5 * time.Hour
	DefaultRolloutTimeout  = 30 * time.Minute
	DefaultCollectInterval = 30 * time.Second
	DefaultTickInterval    = time.Minute
)

type phase int

const (
	phaseRollingOut phase = iota
	phaseEvaluating
)

// Controller is the soak release state machine. Tick and Collect drive it;
// Run calls them on their intervals.
type Controller struct {
	cfg     Config
	store   ParameterStore
	sampler Sampler
	metrics Metrics
	rollout RolloutChecker
	churn   ChurnStats
	log     *slog.Logger
	now     func() time.Time

	phase phase
	// release is the release being rolled out or evaluated, nil during a
	// rolling window between releases.
	release *ReleaseResult
	// handled is the last release ID that reached a final status.
	handled     string
	windowStart time.Time
	samples     []collector.Sample
}

// New returns a Controller. Call Start before Tick or Collect.
func New(cfg Config, store ParameterStore, sampler Sampler, metrics Metrics, rollout RolloutChecker, churnStats ChurnStats, log *slog.Logger) *Controller {
	return &Controller{
		cfg: cfg, store: store, sampler: sampler, metrics: metrics,
		rollout: rollout, churn: churnStats, log: log, now: time.Now,
	}
}

// Start restores state from SSM: a release that already has a final result
// isn't evaluated again, so a controller restart only re-runs a release whose
// window hadn't finished.
func (c *Controller) Start(ctx context.Context) error {
	raw, ok, err := c.store.Get(ctx, c.cfg.ResultParameter)
	if err != nil {
		return fmt.Errorf("reading %s: %w", c.cfg.ResultParameter, err)
	}
	if ok {
		var prev ReleaseResult
		if err := json.Unmarshal([]byte(raw), &prev); err != nil {
			c.log.Warn("ignoring unreadable previous result", "error", err)
		} else if isFinal(prev.Status) {
			c.handled = prev.ReleaseID
		}
	}
	c.startRollingWindow()
	return c.pollRequest(ctx)
}

// Run drives the controller until ctx is cancelled.
func (c *Controller) Run(ctx context.Context, collectInterval, tickInterval time.Duration) error {
	if err := c.Start(ctx); err != nil {
		return err
	}
	collect := time.NewTicker(collectInterval)
	defer collect.Stop()
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-collect.C:
			c.Collect(ctx)
		case <-tick.C:
			c.Tick(ctx)
		}
	}
}

// Collect samples the agents, publishes the samples, and keeps them for the
// current window.
func (c *Controller) Collect(ctx context.Context) {
	samples, err := c.sampler.Collect(ctx)
	if err != nil {
		c.log.Warn("collecting agent samples", "error", err)
	}
	if len(samples) == 0 {
		return
	}
	if err := c.metrics.PublishSamples(ctx, samples); err != nil {
		c.log.Warn("publishing agent samples", "error", err)
	}
	if c.phase == phaseEvaluating {
		c.samples = append(c.samples, samples...)
	}
}

// Tick emits the heartbeat and churn stats, picks up new release requests,
// and advances the current release or window.
func (c *Controller) Tick(ctx context.Context) {
	now := c.now()
	if err := c.metrics.PublishHeartbeat(ctx, now); err != nil {
		c.log.Warn("publishing heartbeat", "error", err)
	}
	if c.churn != nil {
		if err := c.metrics.PublishChurn(ctx, c.churn.Stats(), now); err != nil {
			c.log.Warn("publishing churn stats", "error", err)
		}
	}
	if err := c.pollRequest(ctx); err != nil {
		c.log.Warn("polling release request", "error", err)
	}

	// re-read the clock: polling may have started a release at a later time,
	// and the window must not start before its rollout did.
	now = c.now()
	switch c.phase {
	case phaseRollingOut:
		c.checkRollout(ctx, now)
	case phaseEvaluating:
		if now.Sub(c.windowStart) >= c.cfg.Window {
			c.finishWindow(ctx, now)
		}
	}
}

func (c *Controller) pollRequest(ctx context.Context) error {
	raw, ok, err := c.store.Get(ctx, c.cfg.RequestParameter)
	if err != nil || !ok {
		return err
	}
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return fmt.Errorf("parsing %s: %w", c.cfg.RequestParameter, err)
	}
	if req.ReleaseID == "" || req.ReleaseID == c.handled {
		return nil
	}
	if c.release != nil && c.release.ReleaseID == req.ReleaseID {
		return nil
	}
	now := c.now()
	if c.release != nil {
		c.release.Status = StatusSuperseded
		c.release.Detail = "superseded by release " + req.ReleaseID
		c.writeResult(ctx, c.release, now)
	}
	c.log.Info("starting release", "release_id", req.ReleaseID, "release_version", req.ReleaseVersion, "image_uri", req.ImageURI)
	c.release = &ReleaseResult{Request: req, Status: StatusRollingOut, RolloutStartedAt: now}
	c.phase = phaseRollingOut
	c.samples = nil
	c.writeResult(ctx, c.release, now)
	return nil
}

func (c *Controller) checkRollout(ctx context.Context, now time.Time) {
	done, detail, err := c.rollout.RolledOut(ctx, c.release.ImageURI)
	if err != nil {
		c.log.Warn("checking rollout", "error", err)
	}
	if done {
		c.log.Info("rollout complete, starting window", "release_id", c.release.ReleaseID, "window", c.cfg.Window)
		c.phase = phaseEvaluating
		c.windowStart = now
		c.samples = nil
		c.release.Status = StatusEvaluating
		c.release.WindowStart = &now
		c.writeResult(ctx, c.release, now)
		return
	}
	if now.Sub(c.release.RolloutStartedAt) >= c.cfg.RolloutTimeout {
		c.log.Error("rollout timed out", "release_id", c.release.ReleaseID, "detail", detail)
		c.release.Status = StatusRolloutTimeout
		c.release.Verdict = analysis.VerdictFail
		c.release.Detail = fmt.Sprintf("agent did not roll out within %s: %s", c.cfg.RolloutTimeout, detail)
		c.writeResult(ctx, c.release, now)
		c.handled = c.release.ReleaseID
		c.release = nil
		c.startRollingWindow()
	}
}

func (c *Controller) finishWindow(ctx context.Context, now time.Time) {
	result := analysis.Analyze(c.samples, c.windowStart, now, c.cfg.Thresholds)
	c.log.Info("window finished", "verdict", result.Verdict, "nodes", result.Nodes, "findings", len(result.Findings), "release", c.release != nil)
	for _, f := range result.Findings {
		c.log.Warn("finding", "node", f.Node, "signal", f.Signal, "check", f.Check, "message", f.Message)
	}
	if err := c.metrics.PublishResult(ctx, result, now); err != nil {
		c.log.Warn("publishing window result", "error", err)
	}
	if c.release != nil {
		c.release.Status = StatusComplete
		c.release.Verdict = result.Verdict
		c.release.WindowEnd = &now
		c.release.Analysis = &result
		c.writeResult(ctx, c.release, now)
		c.handled = c.release.ReleaseID
		c.release = nil
	}
	c.startRollingWindow()
}

// startRollingWindow begins an unattended window on whatever agent build is
// running, so leaks between releases are still caught.
func (c *Controller) startRollingWindow() {
	c.phase = phaseEvaluating
	c.windowStart = c.now()
	c.samples = nil
}

func (c *Controller) writeResult(ctx context.Context, result *ReleaseResult, now time.Time) {
	result.UpdatedAt = now
	stored := *result
	if stored.Analysis != nil && len(stored.Analysis.Findings) > maxStoredFindings {
		trimmed := *stored.Analysis
		trimmed.Notes = append(append([]string{}, trimmed.Notes...),
			fmt.Sprintf("%d more findings omitted; see the soak controller logs", len(trimmed.Findings)-maxStoredFindings))
		trimmed.Findings = trimmed.Findings[:maxStoredFindings]
		stored.Analysis = &trimmed
	}
	data, err := json.Marshal(stored)
	if err == nil {
		err = c.store.Put(ctx, c.cfg.ResultParameter, string(data))
	}
	if err != nil {
		// a pipeline should treat a missing or stale result as a failure, so a
		// lost write can block a release but never pass one.
		c.log.Error("writing release result", "release_id", result.ReleaseID, "status", result.Status, "error", err)
	}
}

func isFinal(s Status) bool {
	return s == StatusComplete || s == StatusRolloutTimeout || s == StatusSuperseded
}
