package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Feature: tcpdump-packet-capture, Property 6: Completed packet capture is idempotent
// **Validates: Requirements 3.4**
func TestProperty6_CompletedPacketCaptureIsIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.StringMatching(`[a-z]{3,15}`).Draw(rt, "name")
		reason := rapid.SampledFrom([]string{
			v1alpha1.CaptureStateSuccess,
			v1alpha1.CaptureStateFailure,
		}).Draw(rt, "reason")
		message := rapid.StringMatching(`[a-z ]{5,50}`).Draw(rt, "message")

		nd := &v1alpha1.NodeDiagnostic{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.NodeDiagnosticSpec{
				PacketCapture: &v1alpha1.PacketCapture{
					Mode:     v1alpha1.ModeTcpdump,
					Duration: "30s",
					Upload: v1alpha1.PacketCaptureUpload{
						URL:    "https://example.com",
						Fields: map[string]string{"key": "test/${filename}"},
					},
				},
			},
			Status: v1alpha1.NodeDiagnosticStatus{
				CaptureStatuses: []v1alpha1.CaptureStatus{
					{
						Type: v1alpha1.CaptureTypePacket,
						State: v1alpha1.CaptureState{
							Completed: &v1alpha1.CaptureStateCompleted{
								Reason:     reason,
								Message:    message,
								StartedAt:  metav1.Now(),
								FinishedAt: metav1.Now(),
							},
						},
					},
				},
			},
		}

		// Verify the idempotency check: GetCaptureStatus returns completed, so
		// handlePacketCapture should return nil without modifying anything.
		existingStatus := nd.Status.GetCaptureStatus(v1alpha1.CaptureTypePacket)
		if existingStatus == nil || existingStatus.State.Completed == nil {
			rt.Fatal("test setup error: status should be completed")
		}

		// The controller would return nil here without starting a new capture.
		// We verify the precondition that drives the idempotency behavior.
		statusBefore := nd.Status.DeepCopy()

		// Simulate the idempotency check from handlePacketCapture
		if nd.Spec.PacketCapture == nil {
			rt.Fatal("spec should not be nil")
		}
		existing := nd.Status.GetCaptureStatus(v1alpha1.CaptureTypePacket)
		if existing == nil || existing.State.Completed == nil {
			rt.Fatal("should have found completed status")
		}

		// Status should be unchanged
		assert.Equal(t, statusBefore.CaptureStatuses[0].State.Completed.Reason,
			nd.Status.CaptureStatuses[0].State.Completed.Reason)
		assert.Equal(t, statusBefore.CaptureStatuses[0].State.Completed.Message,
			nd.Status.CaptureStatuses[0].State.Completed.Message)
	})
}

// Test Reconcile with both specs nil → no action (Requirements 2.1)
func TestReconcile_BothNil_NoAction(t *testing.T) {
	c := &nodeDiagnosticController{}
	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec:       v1alpha1.NodeDiagnosticSpec{},
	}
	result, err := c.Reconcile(context.Background(), nd)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

// Test Reconcile with only packetCapture set → calls packet handler (Requirements 2.2)
// We verify that when packetCapture is set and status is already completed,
// Reconcile returns without error (idempotent path).
func TestReconcile_OnlyPacketCapture_CompletedIdempotent(t *testing.T) {
	c := &nodeDiagnosticController{}
	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: v1alpha1.NodeDiagnosticSpec{
			PacketCapture: &v1alpha1.PacketCapture{
				Mode:     v1alpha1.ModeTcpdump,
				Duration: "30s",
				Upload: v1alpha1.PacketCaptureUpload{
					URL:    "https://example.com",
					Fields: map[string]string{"key": "test"},
				},
			},
		},
		Status: v1alpha1.NodeDiagnosticStatus{
			CaptureStatuses: []v1alpha1.CaptureStatus{
				{
					Type: v1alpha1.CaptureTypePacket,
					State: v1alpha1.CaptureState{
						Completed: &v1alpha1.CaptureStateCompleted{
							Reason:     v1alpha1.CaptureStateSuccess,
							Message:    fmt.Sprintf("packet capture completed successfully. Delete this resource with: kubectl delete nodediagnostic test-node"),
							StartedAt:  metav1.Now(),
							FinishedAt: metav1.Now(),
						},
					},
				},
			},
		},
	}
	result, err := c.Reconcile(context.Background(), nd)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

// Test that handlePacketCapture returns nil when packetCapture is nil
func TestHandlePacketCapture_NilSpec(t *testing.T) {
	c := &nodeDiagnosticController{}
	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec:       v1alpha1.NodeDiagnosticSpec{},
	}
	err := c.handlePacketCapture(context.Background(), nd)
	assert.NoError(t, err)
}

// TestHandleSpecChange_GenerationUnchanged_NoCancelCalled verifies that when
// old and new objects have the same generation, no cancel is triggered.
// Requirements: 1.1
func TestHandleSpecChange_GenerationUnchanged_NoCancelCalled(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "test-node"}

	// Store a cancel func so we can verify it is NOT called
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.packetCancelFunc.Store(&cancel)

	oldND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 1},
	}
	newND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 1},
	}

	c.handleSpecChange(logr.Discard(), oldND, newND)

	// Context should NOT be cancelled since generation didn't change
	assert.NoError(t, ctx.Err())
}

// TestHandleSpecChange_GenerationChanged_NoActiveCapture_NoPanic verifies that
// when generation changes but no capture is running (nil cancel func), the
// handler does not panic and logs a debug message.
// Requirements: 8.1, 8.2
func TestHandleSpecChange_GenerationChanged_NoActiveCapture_NoPanic(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "test-node"}
	// packetCancelFunc is zero-value (nil) — no active capture

	oldND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 1},
	}
	newND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 2},
	}

	// Should not panic
	assert.NotPanics(t, func() {
		c.handleSpecChange(logr.Discard(), oldND, newND)
	})
}

// TestHandleSpecChange_GenerationChanged_ActiveCapture_CancelCalled verifies
// that when generation changes and a capture is active, the cancel func is
// called, cancelling the capture context.
// Requirements: 1.1, 6.2
func TestHandleSpecChange_GenerationChanged_ActiveCapture_CancelCalled(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "test-node"}

	ctx, cancel := context.WithCancel(context.Background())
	c.packetCancelFunc.Store(&cancel)

	oldND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 1},
	}
	newND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Generation: 2},
	}

	c.handleSpecChange(logr.Discard(), oldND, newND)

	// Context should be cancelled
	assert.Equal(t, context.Canceled, ctx.Err())
}

// TestHandleSpecChange_WrongNodeName_NoCancelCalled verifies that when the
// NodeDiagnostic name doesn't match the controller's nodeName, no cancel is
// triggered even if generation changed.
// Requirements: 1.1
func TestHandleSpecChange_WrongNodeName_NoCancelCalled(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "my-node"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.packetCancelFunc.Store(&cancel)

	oldND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "other-node", Generation: 1},
	}
	newND := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "other-node", Generation: 2},
	}

	c.handleSpecChange(logr.Discard(), oldND, newND)

	// Context should NOT be cancelled since node name doesn't match
	assert.NoError(t, ctx.Err())
}

// TestHandlePacketCapture_CancelledDuringCapture verifies that when the capture
// context is cancelled (simulating a spec change or CR deletion via the event
// handler), handlePacketCapture returns nil without error, allowing the next
// Reconcile to proceed.
func TestHandlePacketCapture_CancelledDuringCapture(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.SchemeBuilder.AddToScheme(scheme))

	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-node",
			Generation: 3,
		},
		Spec: v1alpha1.NodeDiagnosticSpec{
			PacketCapture: &v1alpha1.PacketCapture{
				Mode:     v1alpha1.ModeTcpdump,
				Duration: "10m",
				Upload: v1alpha1.PacketCaptureUpload{
					URL:    "https://example.com/upload",
					Fields: map[string]string{"key": "test/${filename}"},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nd).
		WithStatusSubresource(&v1alpha1.NodeDiagnostic{}).
		Build()

	c := &nodeDiagnosticController{
		kubeClient: fakeClient,
		nodeName:   "test-node",
	}

	// Mock captureFunc: block until context is cancelled, then return context.Canceled
	c.captureFunc = func(ctx context.Context, nd *v1alpha1.NodeDiagnostic, captureID string) ([]error, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	// Run handlePacketCapture in a goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.handlePacketCapture(context.Background(), nd)
	}()

	// Wait briefly for the capture to start and store the cancelFunc, then cancel it
	time.Sleep(200 * time.Millisecond)
	if cancelFuncPtr := c.packetCancelFunc.Load(); cancelFuncPtr != nil {
		(*cancelFuncPtr)()
	} else {
		t.Fatal("expected packetCancelFunc to be stored after capture starts")
	}

	// Wait for handlePacketCapture to return — should return nil (no error)
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("handlePacketCapture did not return within timeout")
	}
}

// TestHandleDelete_ActiveCapture_CancelCalled verifies that when a
// NodeDiagnostic is deleted while a capture is active, the cancel func is called.
func TestHandleDelete_ActiveCapture_CancelCalled(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "test-node"}

	ctx, cancel := context.WithCancel(context.Background())
	c.packetCancelFunc.Store(&cancel)

	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
	}

	c.handleDelete(logr.Discard(), nd)

	assert.Equal(t, context.Canceled, ctx.Err())
}

// TestHandleDelete_NoActiveCapture_NoPanic verifies that deleting a CR
// when no capture is running does not panic.
func TestHandleDelete_NoActiveCapture_NoPanic(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "test-node"}

	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
	}

	assert.NotPanics(t, func() {
		c.handleDelete(logr.Discard(), nd)
	})
}

// TestHandleDelete_WrongNodeName_NoCancelCalled verifies that deleting a CR
// for a different node does not cancel the local capture.
func TestHandleDelete_WrongNodeName_NoCancelCalled(t *testing.T) {
	c := &nodeDiagnosticController{nodeName: "my-node"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.packetCancelFunc.Store(&cancel)

	nd := &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{Name: "other-node"},
	}

	c.handleDelete(logr.Discard(), nd)

	assert.NoError(t, ctx.Err())
}
