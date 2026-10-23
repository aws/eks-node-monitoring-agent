package autocapture

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
)

// Worker reacts to capture requests from the node exporter. For now it only
// logs what it would capture; collection and upload arrive in a later change.
type Worker struct {
	requests <-chan manager.CaptureRequest
	client   client.Reader
	log      logr.Logger

	// Guard once-only informational logs so repeated requests don't spam.
	loggedOff     bool
	loggedIgnored bool
}

func NewWorker(requests <-chan manager.CaptureRequest, reader client.Reader, log logr.Logger) *Worker {
	return &Worker{
		requests: requests,
		client:   reader,
		log:      log,
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

	w.log.Info("capture requested",
		"condition", req.Condition, "reason", req.Reason,
		"bucket", bucket, "nodeDiagnostic", name)

	// Surface the ambiguity once so an operator knows other buckets are inert.
	if ignored > 0 && !w.loggedIgnored {
		w.log.Info("multiple NodeDiagnostics set spec.autoLogCapture.bucket; using the newest and ignoring the rest",
			"using", name, "ignored", ignored)
		w.loggedIgnored = true
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
