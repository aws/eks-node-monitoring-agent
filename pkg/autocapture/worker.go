package autocapture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/internal/version"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/captureguard"
	fileutil "github.com/aws/eks-node-monitoring-agent/pkg/util/file"
)

// defaultCooldown keeps a flapping condition from uploading a bundle per flip.
const defaultCooldown = 30 * time.Minute

// defaultCaptureTimeout only bounds a stuck or very large capture.
const defaultCaptureTimeout = 10 * time.Minute

// maxCaptureBytes bounds the S3 storage and transfer cost of one capture. It
// counts compressed bytes, as they reach S3. The timer normally ends a huge
// capture first; files already uploaded are kept.
const maxCaptureBytes = 5 << 30

const skipReasonSizeCap = "size cap"

// errUploadDenied cancels a capture, so handle can tell it from the timer.
var errUploadDenied = errors.New("upload denied")

// Worker collects logs for capture requests from the node exporter.
type Worker struct {
	requests <-chan manager.CaptureRequest
	client   client.Reader
	nodeName string
	log      logr.Logger

	// A field so tests can fake it.
	newUploader func(ctx context.Context) (objectUploader, error)
	// Only a successful build is kept: a transient IMDS failure must not
	// disable uploads until restart, and captures are rare, so retrying is cheap.
	// No lock needed: only handle touches it, on Start's one goroutine.
	uploader objectUploader

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
	// A field so tests can reach the cap without gigabytes of data.
	captureBudget int64
	// Kept in memory, not persisted: after a restart the worst case is one
	// extra capture per still-failing condition, not worth persisting for.
	// No lock needed: only handle touches the map, on Start's one goroutine.
	lastCapture map[corev1.NodeConditionType]time.Time

	// A field so tests can fake it.
	collect func(ctx context.Context, categories []v1alpha1.LogCategory, trigger Trigger, handle StepFunc) (Summary, error)
}

func NewWorker(requests <-chan manager.CaptureRequest, reader client.Reader, runtimeContext *config.RuntimeContext, guard *captureguard.Guard, nodeName string, log logr.Logger) *Worker {
	return &Worker{
		requests:       requests,
		client:         reader,
		nodeName:       nodeName,
		log:            log,
		newUploader:    newS3Uploader,
		guard:          guard,
		cooldown:       defaultCooldown,
		now:            time.Now,
		captureTimeout: defaultCaptureTimeout,
		captureBudget:  maxCaptureBytes,
		lastCapture:    make(map[corev1.NodeConditionType]time.Time),
		collect: func(ctx context.Context, categories []v1alpha1.LogCategory, trigger Trigger, handle StepFunc) (Summary, error) {
			return CollectSteps(ctx, runtimeContext, categories, trigger, handle)
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
	// Before the cooldown, so a capture that could not be uploaded does not burn it.
	if w.uploader == nil {
		up, err := w.newUploader(ctx)
		if err != nil {
			w.log.Error(err, "upload unavailable", "condition", req.Condition, "reason", req.Reason)
			return
		}
		w.uploader = up
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

	// The timer is outermost so handle can still wait on it after a denial
	// cancels ctxCap.
	timerCtx, cancel := context.WithTimeout(ctx, w.captureTimeout)
	defer cancel()
	ctxCap, cancelCap := context.WithCancelCause(timerCtx)
	defer cancelCap(nil)
	started := w.now()
	// One time for the whole capture, so all its steps share one key prefix.
	at := req.At.UTC()
	if req.At.IsZero() {
		at = started.UTC()
	}

	trigger := Trigger{
		Condition:  req.Condition,
		Reason:     req.Reason,
		Message:    req.Message,
		At:         req.At,
		Node:       w.nodeName,
		NMAVersion: version.String(),
	}

	// Local to this capture; handle reads them only after the result arrives.
	var (
		used     int64
		uploaded int
		// collectSteps cannot see steps the size cap skipped.
		sizeCapped []string
	)
	uploadStep := func(_ context.Context, step Step) error {
		// Per-collector detail is for debugging; the end summary is what
		// an operator needs.
		w.log.V(3).Info("collector collected",
			"condition", req.Condition, "reason", req.Reason, "step", step.Name,
			"files", step.Files, "bytes", step.Bytes, "failed", step.Failed,
			"seconds", step.Duration.Seconds())
		// StreamTarGzip treats zero or less as no cap, so never pass it.
		remaining := w.captureBudget - used
		if remaining <= 0 {
			sizeCapped = append(sizeCapped, step.Name)
			return nil
		}

		stepStarted := w.now()
		key := stepKey(w.nodeName, at, string(req.Condition), step.Name)
		stream := fileutil.StreamTarGzip(ctxCap, step.Dir, remaining)
		defer stream.Close()

		err := w.uploader.Upload(ctxCap, bucket, key, stream)
		switch {
		case err == nil:
			used += stream.Written()
			uploaded++
			w.log.V(3).Info("collector uploaded",
				"condition", req.Condition, "step", step.Name, "key", key,
				"compressedBytes", stream.Written(), "skippedFiles", len(stream.Skipped()),
				"seconds", w.now().Sub(stepStarted).Seconds())
			return nil
		case errors.Is(err, fileutil.ErrBundleTooLarge):
			// A smaller later step may still fit.
			w.log.Info("step skipped: size cap", "condition", req.Condition, "step", step.Name)
			sizeCapped = append(sizeCapped, step.Name)
			return nil
		case isAccessDenied(err):
			// The error body adds nothing; the bucket is what the operator must fix.
			w.log.Error(nil, "upload denied, stopping uploads",
				"condition", req.Condition, "bucket", bucket)
			// Every later step would be denied too, so stop collecting them.
			cancelCap(errUploadDenied)
			return err
		default:
			w.log.Error(err, "failed to upload step", "condition", req.Condition, "step", step.Name, "key", key)
			return err
		}
	}

	type collectResult struct {
		summary Summary
		err     error
	}
	// Buffered so the send never blocks after handle stops waiting.
	done := make(chan collectResult, 1)
	go func() {
		summary, err := w.collect(ctxCap, CategoriesFor(req.Condition), trigger, uploadStep)
		// Released here, before the send, so whoever gets the result sees the
		// guard free. Never defer it in handle: a timeout returns while this runs.
		w.guard.Release()
		done <- collectResult{summary: summary, err: err}
	}()

	abandon := func() {
		// Parent canceled means the agent is stopping; otherwise our timeout fired.
		if ctx.Err() != nil {
			w.log.Info("capture stopped",
				"condition", req.Condition, "reason", req.Reason)
		} else {
			w.log.Info("capture timed out, collector may be blocked on host I/O",
				"condition", req.Condition, "reason", req.Reason)
		}
		// The collector stops at its next ctx check and cleans up its own folder.
	}

	var res collectResult
	select {
	case <-ctxCap.Done():
		if !errors.Is(context.Cause(ctxCap), errUploadDenied) {
			abandon()
			return
		}
		// A denial cancels ctxCap while collect is still returning. Wait for
		// it, still bounded by the timer, so the counts are read only after it.
		select {
		case <-timerCtx.Done():
			abandon()
			return
		case res = <-done:
		}
	case res = <-done:
	}

	if errors.Is(res.err, ErrLowDisk) {
		// Keep the cooldown entry: a full disk won't clear soon, so a retry
		// would only write the trigger note again.
		w.log.Info("capture not collected: not enough free disk",
			"condition", req.Condition, "reason", req.Reason, "minFreePercent", minFreePercent)
		return
	}
	// A denial's cancel error is not a collect failure; it was logged in uploadStep.
	stoppedByDenial := errors.Is(context.Cause(ctxCap), errUploadDenied) && errors.Is(res.err, context.Canceled)
	if res.err != nil && !stoppedByDenial {
		w.log.Error(res.err, "failed to collect logs for capture",
			"condition", req.Condition, "reason", req.Reason)
		return
	}
	w.log.Info("capture finished",
		"condition", req.Condition, "reason", req.Reason, "nodeDiagnostic", name,
		"files", res.summary.Files, "bytes", res.summary.Bytes,
		"uploadedBytes", used, "uploaded", uploaded, "failedUploads", res.summary.HandleErrors,
		"failedCollectors", res.summary.Failed,
		"skippedCollectors", len(res.summary.Skipped)+len(sizeCapped),
		"seconds", w.now().Sub(started).Seconds())

	if len(res.summary.Skipped) > 0 || len(sizeCapped) > 0 {
		skipped := make([]string, 0, len(res.summary.Skipped)+len(sizeCapped))
		for _, s := range res.summary.Skipped {
			skipped = append(skipped, fmt.Sprintf("%s: %s", s.Name, s.Reason))
		}
		for _, n := range sizeCapped {
			skipped = append(skipped, fmt.Sprintf("%s: %s", n, skipReasonSizeCap))
		}
		w.log.Info("capture stopped early",
			"condition", req.Condition, "reason", req.Reason, "skipped", skipped)
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
