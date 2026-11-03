package autocapture

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
)

// FindBucket returns the bucket for automatic log capture and the name of the
// NodeDiagnostic it came from, or ("", "", 0, nil) when none sets a bucket.
//
// When several set a bucket, the newest wins (ties broken by name) so the
// choice is deterministic rather than list-order dependent. ignored counts the
// other candidates, so a caller can warn that some objects were passed over.
func FindBucket(ctx context.Context, c client.Reader) (bucket, name string, ignored int, err error) {
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
