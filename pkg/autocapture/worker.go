package autocapture

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/captureguard"
)

// defaultCooldown keeps a flapping condition from uploading a bundle per flip.
const defaultCooldown = 30 * time.Minute

// defaultCaptureTimeout only bounds a stuck or very large capture.
const defaultCaptureTimeout = 10 * time.Minute

// Worker collects logs for capture requests from the node exporter.
type Worker struct {
	requests <-chan manager.CaptureRequest
	client   client.Reader
	log      logr.Logger

	// guard is shared with the NodeDiagnostic controller so the two never
	// collect at once on an already-unhealthy node.
	guard *captureguard.Guard

	// Guard once-only informational logs so repeated requests don't spam.
	loggedOff     bool
	loggedIgnored bool

	cooldown time.Duration
	now      func() time.Time

	// A field so tests can use a short timeout.
	captureTimeout time.Duration
	// Kept in memory, not persisted: after a restart the worst case is one
	// extra capture per still-failing condition, not worth persisting for.
	// No lock needed: only handle touches the map, on Start's one goroutine.
	lastCapture map[corev1.NodeConditionType]time.Time

	// A field so tests can fake it.
	collect func(ctx context.Context, categories []v1alpha1.LogCategory, handle StepFunc) (Summary, error)
}

func NewWorker(requests <-chan manager.CaptureRequest, reader client.Reader, runtimeContext *config.RuntimeContext, guard *captureguard.Guard, log logr.Logger) *Worker {
	return &Worker{
		requests:       requests,
		client:         reader,
		log:            log,
		guard:          guard,
		cooldown:       defaultCooldown,
		now:            time.Now,
		captureTimeout: defaultCaptureTimeout,
		lastCapture:    make(map[corev1.NodeConditionType]time.Time),
		collect: func(ctx context.Context, categories []v1alpha1.LogCategory, handle StepFunc) (Summary, error) {
			return CollectSteps(ctx, runtimeContext, categories, handle)
		},
	}
}

// Start runs until the context is canceled. It always returns nil: as a
// manager Runnable, a returned error would stop the whole agent, and one
// failed capture must never take monitoring down.
func (w *Worker) Start(ctx context.Context) error {
	w.log.Info("starting auto log capture worker")
	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-w.requests:
			w.handle(ctx, req)
		}
	}
}

// handle processes one request. Errors are logged and the request dropped:
// there's nothing to retry against, since the next failure sends a fresh one.
func (w *Worker) handle(ctx context.Context, req manager.CaptureRequest) {
	bucket, name, ignored, err := findBucket(ctx, w.client)
	if err != nil {
		w.log.Error(err, "failed to look up the automatic log capture bucket",
			"condition", req.Condition, "reason", req.Reason)
		return
	}
	if bucket == "" {
		if !w.loggedOff {
			w.log.Info("auto log capture is off: no NodeDiagnostic sets spec.autoLogCapture.bucket")
			w.loggedOff = true
		}
		return
	}

	// Checked only after a bucket is found, so an off worker never burns the
	// cooldown window on a request it couldn't have acted on anyway.
	now := w.now()
	if last, ok := w.lastCapture[req.Condition]; ok {
		if remaining := w.cooldown - now.Sub(last); remaining > 0 {
			w.log.V(1).Info("capture skipped: cooldown active",
				"condition", req.Condition, "remaining", remaining)
			return
		}
	}
	w.lastCapture[req.Condition] = now

	w.log.Info("capture requested",
		"condition", req.Condition, "reason", req.Reason,
		"bucket", bucket, "nodeDiagnostic", name)

	// Surface the ambiguity once so an operator knows other buckets are inert.
	if ignored > 0 && !w.loggedIgnored {
		w.log.Info("multiple NodeDiagnostics set spec.autoLogCapture.bucket; using the newest and ignoring the rest",
			"using", name, "ignored", ignored)
		w.loggedIgnored = true
	}

	// The NodeDiagnostic controller shares this guard, so their load never stacks.
	if !w.guard.TryAcquire() {
		w.log.Info("capture skipped: another log collection is running",
			"condition", req.Condition, "reason", req.Reason)
		// Nothing was collected, so don't let this skip start a cooldown.
		delete(w.lastCapture, req.Condition)
		return
	}

	ctxCap, cancel := context.WithTimeout(ctx, w.captureTimeout)
	defer cancel()
	started := w.now()

	type collectResult struct {
		summary Summary
		err     error
	}
	// Buffered so the send never blocks after handle stops waiting.
	done := make(chan collectResult, 1)
	go func() {
		summary, err := w.collect(ctxCap, CategoriesFor(req.Condition), func(_ context.Context, step Step) error {
			// Per-collector detail is for debugging; the end summary is what
			// an operator needs.
			w.log.V(3).Info("collector collected",
				"condition", req.Condition, "reason", req.Reason, "step", step.Name,
				"files", step.Files, "bytes", step.Bytes, "failed", step.Failed,
				"seconds", step.Duration.Seconds())
			return nil
		})
		// Released here, before the send, so whoever gets the result sees the
		// guard free. Never defer it in handle: a timeout returns while this runs.
		w.guard.Release()
		done <- collectResult{summary: summary, err: err}
	}()

	select {
	case <-ctxCap.Done():
		// Parent canceled means the agent is stopping; otherwise our timeout fired.
		if ctx.Err() != nil {
			w.log.Info("capture stopped",
				"condition", req.Condition, "reason", req.Reason)
		} else {
			w.log.Info("capture timed out, collector may be blocked on host I/O",
				"condition", req.Condition, "reason", req.Reason)
		}
		// The collector stops at its next ctx check and cleans up its own folder.
		return
	case res := <-done:
		if errors.Is(res.err, ErrLowDisk) {
			// Keep the cooldown entry: a full disk won't clear soon,
			// so a retry would only skip again.
			w.log.Info("capture skipped: not enough free disk",
				"condition", req.Condition, "reason", req.Reason, "minFreePercent", minFreePercent)
			return
		}
		if res.err != nil {
			w.log.Error(res.err, "failed to collect logs for capture",
				"condition", req.Condition, "reason", req.Reason)
			return
		}
		w.log.Info("capture collected",
			"condition", req.Condition, "reason", req.Reason, "nodeDiagnostic", name,
			"files", res.summary.Files, "bytes", res.summary.Bytes,
			"failedCollectors", res.summary.Failed, "skippedCollectors", len(res.summary.Skipped),
			"seconds", w.now().Sub(started).Seconds())
	}
}

// findBucket returns the bucket for automatic log capture and the name of the
// NodeDiagnostic it came from, or ("", "", 0, nil) when none sets a bucket.
//
// When several set a bucket, the newest wins (ties broken by name) so the
// choice is deterministic rather than list-order dependent. ignored counts the
// other candidates, so a caller can warn that some objects were passed over.
func findBucket(ctx context.Context, c client.Reader) (bucket, name string, ignored int, err error) {
	var list v1alpha1.NodeDiagnosticList
	if err := c.List(ctx, &list); err != nil {
		return "", "", 0, err
	}

	var chosen *v1alpha1.NodeDiagnostic
	matches := 0
	for i := range list.Items {
		nd := &list.Items[i]
		if nd.Spec.AutoLogCapture == nil || nd.Spec.AutoLogCapture.Bucket == "" {
			continue
		}
		matches++
		if chosen == nil || isNewer(nd, chosen) {
			chosen = nd
		}
	}

	if chosen == nil {
		return "", "", 0, nil
	}
	return chosen.Spec.AutoLogCapture.Bucket, chosen.Name, matches - 1, nil
}

func isNewer(a, b *v1alpha1.NodeDiagnostic) bool {
	at := a.CreationTimestamp.Time
	bt := b.CreationTimestamp.Time
	if at.Equal(bt) {
		return a.Name > b.Name // tie-break with name for a stable choice
	}
	return at.After(bt)
}
