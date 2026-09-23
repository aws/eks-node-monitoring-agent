package corefile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

type mockManager struct {
	obs observer.BaseObserver
	res chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), nil
}

func (m *mockManager) Notify(_ context.Context, c monitor.Condition) error {
	m.res <- c
	return nil
}

func newTestManager() *mockManager {
	return &mockManager{res: make(chan monitor.Condition, 5)}
}

// newDetectorWithURL constructs a Detector pointed at a test server URL.
func newDetectorWithURL(mgr *mockManager, url string) *Detector {
	d := New(mgr, logr.Discard())
	d.url = url
	return d
}

func writeReadyz(w http.ResponseWriter, status int, reason, detail, appliedSHA, failedSHA string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(readyzResponse{
		Reason:     reason,
		Detail:     detail,
		AppliedSHA: appliedSHA,
		FailedSHA:  failedSHA,
		NodeName:   "i-test",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	})
}

func TestHandleState_Applied200_DoesNotEmit(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusOK, "Applied", "boot-time apply succeeded", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", "")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no emit for 200 Applied; got %+v", c)
	default:
	}
}

func TestHandleState_ConfigMapNotFound200_DoesNotEmit(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusOK, "CoreDNSCorefileConfigMapNotFound", "customer not opted in", "", "")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no emit for 200 ConfigMapNotFound; got %+v", c)
	default:
	}
}

func TestHandleState_ValidationFailed503_EmitsWarning(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileValidationFailed", "bind directive must include cluster DNS IP", "", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-mgr.res
	assert.Equal(t, reasons.CoreDNSCorefileValidationFailed.Template(), got.Reason)
	assert.Equal(t, monitor.SeverityWarning, got.Severity)
	assert.Contains(t, got.Message, "bind directive")
	assert.Contains(t, got.Message, "failedSha=1234567890ab")
}

func TestHandleState_RevertedToBaseline503_EmitsWarning(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileRevertedToBaseline", "customer Corefile did not reach /ready", "", "")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-mgr.res
	assert.Equal(t, reasons.CoreDNSCorefileRevertedToBaseline.Template(), got.Reason)
	// Warning, not Fatal: baseline DNS is serving, so the node is functional;
	// no node-repair signal warranted for a customer-config failure.
	assert.Equal(t, monitor.SeverityWarning, got.Severity)
}

func TestHandleState_UnknownReason_DoesNotEmit(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileInitializing", "agent starting", "", "")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no emit for unmapped reason; got %+v", c)
	default:
	}
}

func TestHandleState_MalformedJSON_DoesNotEmit(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "not-json")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no emit for malformed JSON; got %+v", c)
	default:
	}
}

// TestHandleState_RevertedToLastAppliedLabelsAppliedSHA covers the other half
// of the split: a revert reports the Corefile now serving, so the message must
// not label it as the failed one.
func TestHandleState_RevertedToLastAppliedLabelsAppliedSHA(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileRevertedToLastApplied",
			"readiness: /ready returned 503", "fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321", "")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-mgr.res
	assert.Contains(t, got.Message, "appliedSha=fedcba098765")
	assert.NotContains(t, got.Message, "failedSha")
}

// TestHandleState_ShortSHAOmitted guards the truncation length check: a SHA
// shorter than the truncation width must not slice out of range.
func TestHandleState_ShortSHAOmitted(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileReloadFailed", "reload rejected", "", "abc123")
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-mgr.res
	assert.Equal(t, "reload rejected", got.Message)
}

func TestHandleState_TransportError_DoesNotEmit(t *testing.T) {
	mgr := newTestManager()
	// Bad URL guarantees a transport error.
	if err := newDetectorWithURL(mgr, "http://127.0.0.1:1").HandleState(); err != nil {
		t.Fatalf("transport failure must be swallowed; got %v", err)
	}
	select {
	case c := <-mgr.res:
		t.Fatalf("expected no emit on transport error; got %+v", c)
	default:
	}
}
