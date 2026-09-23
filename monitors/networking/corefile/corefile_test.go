package corefile

import (
	"context"
	"encoding/json"
	"errors"
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
	obs       observer.BaseObserver
	res       chan monitor.Condition
	notifyErr error
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), nil
}

func (m *mockManager) Notify(_ context.Context, c monitor.Condition) error {
	if m.notifyErr != nil {
		return m.notifyErr
	}
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
func TestHandleState_RevertedToLastAppliedLabelsBothSHAs(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, "CoreDNSCorefileRevertedToLastApplied",
			"readiness: /ready returned 503", "fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321", shaB)
	}))
	defer srv.Close()

	if err := newDetectorWithURL(mgr, srv.URL).HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-mgr.res
	assert.Contains(t, got.Message, "(appliedSha=fedcba098765, failedSha=bbbbbbbbbbbb)")
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

// readyzState is what the fake agent serves; tests mutate it between ticks.
type readyzState struct {
	status                        int
	reason, appliedSHA, failedSHA string
}

func newSequenceDetector(t *testing.T, mgr *mockManager, st *readyzState) (*Detector, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, st.status, st.reason, "detail", st.appliedSHA, st.failedSHA)
	}))
	t.Cleanup(srv.Close)
	clock := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	d := newDetectorWithURL(mgr, srv.URL)
	d.now = func() time.Time { return clock }
	return d, &clock
}

func tickCount(t *testing.T, d *Detector, mgr *mockManager) int {
	t.Helper()
	if err := d.HandleState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n := 0
	for {
		select {
		case <-mgr.res:
			n++
		default:
			return n
		}
	}
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestHandleState_UnchangedFailureNotifiesOnceUntilReNotifyInterval(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileValidationFailed", failedSHA: shaA}
	d, clock := newSequenceDetector(t, mgr, st)

	assert.Equal(t, 1, tickCount(t, d, mgr))
	for i := 0; i < 5; i++ {
		*clock = clock.Add(30 * time.Second)
		assert.Equal(t, 0, tickCount(t, d, mgr), "tick %d re-sent an unchanged failure", i)
	}
	*clock = clock.Add(reNotifyInterval)
	assert.Equal(t, 1, tickCount(t, d, mgr))
}

func TestHandleState_ReasonOrSHAChangeNotifiesImmediately(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileValidationFailed", failedSHA: shaA}
	d, _ := newSequenceDetector(t, mgr, st)

	assert.Equal(t, 1, tickCount(t, d, mgr))
	st.failedSHA = shaB
	assert.Equal(t, 1, tickCount(t, d, mgr), "new rejected Corefile must notify")
	st.reason, st.failedSHA, st.appliedSHA = "CoreDNSCorefileRevertedToLastApplied", "", shaA
	assert.Equal(t, 1, tickCount(t, d, mgr), "new reason must notify")
}

func TestHandleState_RecoveryResetsSoRecurrenceNotifies(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileValidationFailed", failedSHA: shaA}
	d, _ := newSequenceDetector(t, mgr, st)

	assert.Equal(t, 1, tickCount(t, d, mgr))
	st.status, st.reason, st.failedSHA, st.appliedSHA = http.StatusOK, "Applied", "", shaB
	assert.Equal(t, 0, tickCount(t, d, mgr))
	st.status, st.reason, st.failedSHA, st.appliedSHA = http.StatusServiceUnavailable, "CoreDNSCorefileValidationFailed", shaA, ""
	assert.Equal(t, 1, tickCount(t, d, mgr))
}

// Two different failed edits that both revert to the same serving Corefile
// must each notify.
func TestHandleState_NewFailedEditRevertingToSameCorefileNotifies(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileRevertedToLastApplied", appliedSHA: shaA, failedSHA: shaB}
	d, _ := newSequenceDetector(t, mgr, st)

	assert.Equal(t, 1, tickCount(t, d, mgr))
	st.failedSHA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	assert.Equal(t, 1, tickCount(t, d, mgr))
}

func TestHandleState_AgentRestartDoesNotReNotify(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileValidationFailed", failedSHA: shaA}
	d, _ := newSequenceDetector(t, mgr, st)

	assert.Equal(t, 1, tickCount(t, d, mgr))
	st.reason, st.failedSHA = "CoreDNSCorefileInitializing", ""
	assert.Equal(t, 0, tickCount(t, d, mgr))
	st.reason, st.failedSHA = "CoreDNSCorefileValidationFailed", shaA
	assert.Equal(t, 0, tickCount(t, d, mgr))
}

func TestHandleState_FailedNotifyRetriesNextTick(t *testing.T) {
	mgr := newTestManager()
	st := &readyzState{status: http.StatusServiceUnavailable, reason: "CoreDNSCorefileValidationFailed", failedSHA: shaA}
	d, _ := newSequenceDetector(t, mgr, st)

	mgr.notifyErr = errors.New("apiserver unavailable")
	assert.Error(t, d.HandleState())
	mgr.notifyErr = nil
	assert.Equal(t, 1, tickCount(t, d, mgr))
}
