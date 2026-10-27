package autocapture

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/captureguard"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.SchemeBuilder.AddToScheme(scheme))
	return scheme
}

func autoLogDiagnostic(name, bucket string, created time.Time) *v1alpha1.NodeDiagnostic {
	return &v1alpha1.NodeDiagnostic{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: v1alpha1.NodeDiagnosticSpec{
			AutoLogCapture: &v1alpha1.AutoLogCapture{Bucket: bucket},
		},
	}
}

func TestFindBucket(t *testing.T) {
	tests := []struct {
		name        string
		objects     []client.Object
		wantBucket  string
		wantName    string
		wantIgnored int
	}{
		{
			name: "no NodeDiagnostics",
		},
		{
			name:       "single bucket returned",
			objects:    []client.Object{autoLogDiagnostic("alpha", "my-bucket", time.Unix(100, 0))},
			wantBucket: "my-bucket",
			wantName:   "alpha",
		},
		{
			// the newest object's bucket must win
			name: "newest wins",
			objects: []client.Object{
				autoLogDiagnostic("older-name", "old-bucket", time.Unix(100, 0)),
				autoLogDiagnostic("newer-name", "new-bucket", time.Unix(200, 0)),
			},
			wantBucket:  "new-bucket",
			wantName:    "newer-name",
			wantIgnored: 1,
		},
		{
			// equal timestamps: the lexically larger name wins
			name: "tie broken by larger name",
			objects: []client.Object{
				autoLogDiagnostic("aaa", "a-bucket", time.Unix(100, 0)),
				autoLogDiagnostic("zzz", "z-bucket", time.Unix(100, 0)),
			},
			wantBucket:  "z-bucket",
			wantName:    "zzz",
			wantIgnored: 1,
		},
		{
			// object with an empty bucket is not a match
			name: "empty bucket skipped",
			objects: []client.Object{
				autoLogDiagnostic("alpha", "", time.Unix(100, 0)),
				autoLogDiagnostic("beta", "real-bucket", time.Unix(200, 0)),
			},
			wantBucket: "real-bucket",
			wantName:   "beta",
		},
		{
			// object with only logCapture is not a match
			name: "logCapture only skipped",
			objects: []client.Object{
				&v1alpha1.NodeDiagnostic{
					ObjectMeta: metav1.ObjectMeta{Name: "log-only", CreationTimestamp: metav1.NewTime(time.Unix(50, 0))},
					Spec: v1alpha1.NodeDiagnosticSpec{
						LogCapture: &v1alpha1.LogCapture{UploadDestination: "node"},
					},
				},
				autoLogDiagnostic("auto", "real-bucket", time.Unix(200, 0)),
			},
			wantBucket: "real-bucket",
			wantName:   "auto",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tc.objects...).Build()

			bucket, name, ignored, err := findBucket(context.Background(), c)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBucket, bucket)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantIgnored, ignored)
		})
	}
}

func TestFindBucket_ListErrorReturned(t *testing.T) {
	wantErr := errors.New("list failed")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return wantErr
			},
		}).
		Build()

	bucket, name, ignored, err := findBucket(context.Background(), c)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, "", bucket)
	assert.Equal(t, "", name)
	assert.Equal(t, 0, ignored)
}

// countingLogger records each emitted message so tests can assert how many
// times a given line was logged.
func countingLogger(counts map[string]int) logr.Logger {
	return logr.New(&countingSink{counts: counts})
}

// countingLoggerWithErrors is countingLogger that also keeps each Error call's error.
func countingLoggerWithErrors(counts map[string]int, errs map[string]error) logr.Logger {
	return logr.New(&countingSink{counts: counts, errs: errs})
}

type countingSink struct {
	counts map[string]int
	// errs is optional; when set, Error also keeps the error for each message.
	errs map[string]error
}

func (s *countingSink) Init(logr.RuntimeInfo)                 {}
func (s *countingSink) Enabled(int) bool                      { return true }
func (s *countingSink) WithValues(...any) logr.LogSink        { return s }
func (s *countingSink) WithName(string) logr.LogSink          { return s }
func (s *countingSink) Info(level int, msg string, kv ...any) { s.counts[msg]++ }
func (s *countingSink) Error(err error, msg string, kv ...any) {
	s.counts[msg]++
	if s.errs != nil {
		s.errs[msg] = err
	}
}

const (
	msgOff       = "auto log capture is off: no NodeDiagnostic sets spec.autoLogCapture.bucket"
	msgRequest   = "capture requested"
	msgSkip      = "capture skipped: cooldown active"
	msgSkipGuard = "capture skipped: another log collection is running"
	msgCollected = "capture collected"
	msgStep      = "collector collected"
	msgSkipDisk  = "capture not collected: not enough free disk"
	msgTimedOut  = "capture timed out, collector may be blocked on host I/O"
	msgFailed    = "failed to collect logs for capture"
)

// collectFunc is the shape of Worker.collect.
type collectFunc = func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error)

// stubCollect fakes w.collect: it counts calls and reports nSteps steps.
func stubCollect(nSteps int) (fn collectFunc, calls *int) {
	n := 0
	fn = func(ctx context.Context, _ []v1alpha1.LogCategory, _ Trigger, handle StepFunc) (Summary, error) {
		n++
		var summary Summary
		for i := 0; i < nSteps; i++ {
			step := Step{Name: "Base/step" + strconv.Itoa(i), Files: 2, Bytes: 4}
			if err := handle(ctx, step); err != nil {
				return summary, err
			}
			summary.Steps++
			summary.Files += step.Files
			summary.Bytes += step.Bytes
		}
		return summary, nil
	}
	return fn, &n
}

func TestWorker_NoBucketLogsOffOnce(t *testing.T) {
	counts := map[string]int{}
	ch := make(chan manager.CaptureRequest, 2)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	w := NewWorker(ch, c, nil, &captureguard.Guard{}, "node-1", countingLogger(counts))
	collectFn, calls := stubCollect(1)
	w.collect = collectFn

	// Two requests with no bucket configured: the "off" message logs once.
	req := manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down", At: time.Now()}
	w.handle(context.Background(), req)
	w.handle(context.Background(), req)

	assert.Equal(t, 1, counts[msgOff], "off message must be logged once for repeated requests")
	assert.Equal(t, 0, counts[msgRequest])
	assert.Equal(t, 0, *calls, "no bucket must not trigger collection")
}

func TestWorker_BucketHandlesRequest(t *testing.T) {
	counts := map[string]int{}
	ch := make(chan manager.CaptureRequest, 1)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(autoLogDiagnostic("alpha", "my-bucket", time.Unix(100, 0))).Build()
	w := NewWorker(ch, c, nil, &captureguard.Guard{}, "node-1", countingLogger(counts))
	collectFn, _ := stubCollect(0)
	w.collect = collectFn

	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down", At: time.Now()})

	assert.Equal(t, 1, counts[msgRequest], "a configured bucket must produce a capture-requested log")
	assert.Equal(t, 0, counts[msgOff])
}

func TestWorker_StartReturnsNilOnCancel(t *testing.T) {
	ch := make(chan manager.CaptureRequest)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	w := NewWorker(ch, c, nil, &captureguard.Guard{}, "node-1", logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "Start must return nil when the context is canceled")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}
}

func TestWorker_Cooldown(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	const condA = "NetworkingReady"
	const condB = "StorageReady"

	// step is one request: at offset from base, for a condition.
	type step struct {
		offset    time.Duration
		condition corev1.NodeConditionType
	}
	tests := []struct {
		name        string
		steps       []step
		wantRequest int
		wantSkip    int
	}{
		{
			name: "second request for same condition inside window is dropped",
			steps: []step{
				{0, condA},
				{29 * time.Minute, condA},
			},
			wantRequest: 1,
			wantSkip:    1,
		},
		{
			name: "request exactly at the window is handled",
			steps: []step{
				{0, condA},
				{30 * time.Minute, condA},
			},
			wantRequest: 2,
		},
		{
			name: "request after the window is handled",
			steps: []step{
				{0, condA},
				{31 * time.Minute, condA},
			},
			wantRequest: 2,
		},
		{
			name: "different conditions do not block each other",
			steps: []step{
				{0, condA},
				{1 * time.Minute, condB},
			},
			wantRequest: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			counts := map[string]int{}
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithObjects(autoLogDiagnostic("alpha", "my-bucket", time.Unix(100, 0))).Build()
			w := NewWorker(make(chan manager.CaptureRequest), c, nil, &captureguard.Guard{}, "node-1", countingLogger(counts))
			collectFn, calls := stubCollect(0)
			w.collect = collectFn

			var cur time.Time
			w.now = func() time.Time { return cur }
			for _, s := range tc.steps {
				cur = base.Add(s.offset)
				w.handle(context.Background(), manager.CaptureRequest{Condition: s.condition, Reason: "Down"})
			}

			assert.Equal(t, tc.wantRequest, counts[msgRequest], "capture-requested count")
			assert.Equal(t, tc.wantSkip, counts[msgSkip], "cooldown-skip count")
			assert.Equal(t, tc.wantRequest, *calls, "a request inside the cooldown must not collect")
		})
	}
}

func TestWorker_NoBucketDoesNotStartCooldown(t *testing.T) {
	counts := map[string]int{}
	// A NodeDiagnostic whose bucket we flip on later, backing the same client.
	nd := autoLogDiagnostic("alpha", "", time.Unix(100, 0))
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(nd).Build()
	w := NewWorker(make(chan manager.CaptureRequest), c, nil, &captureguard.Guard{}, "node-1", countingLogger(counts))
	collectFn, _ := stubCollect(0)
	w.collect = collectFn

	cur := time.Unix(1_000_000, 0)
	w.now = func() time.Time { return cur }

	// First request: no bucket, so nothing is captured and no cooldown starts.
	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})
	assert.Equal(t, 0, counts[msgRequest])

	// Configure the bucket, then a request one minute later must be handled,
	// proving the earlier no-bucket request did not start a cooldown.
	nd.Spec.AutoLogCapture.Bucket = "my-bucket"
	require.NoError(t, c.Update(context.Background(), nd))

	cur = cur.Add(1 * time.Minute)
	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})
	assert.Equal(t, 1, counts[msgRequest], "a request after a bucket is set must be handled")
	assert.Equal(t, 0, counts[msgSkip])
}

func bucketClient(t *testing.T) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(autoLogDiagnostic("alpha", "my-bucket", time.Unix(100, 0))).Build()
}

func TestWorker_LogsEachCollectorAndTotal(t *testing.T) {
	counts := map[string]int{}
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", countingLogger(counts))
	collectFn, calls := stubCollect(2)
	w.collect = collectFn

	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})

	assert.Equal(t, 1, *calls, "collect must be called once")
	assert.Equal(t, 2, counts[msgStep], "each finished collector must be logged")
	assert.Equal(t, 1, counts[msgCollected], "a successful collect must log 'capture collected' once")
}

func TestWorker_PassesTriggerToCollect(t *testing.T) {
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", logr.Discard())
	var got Trigger
	w.collect = func(_ context.Context, _ []v1alpha1.LogCategory, trigger Trigger, _ StepFunc) (Summary, error) {
		got = trigger
		return Summary{}, nil
	}

	at := time.Unix(1_000_000, 0)
	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down", Message: "eth0 is down", At: at})

	assert.Equal(t, corev1.NodeConditionType("NetworkingReady"), got.Condition)
	assert.Equal(t, "Down", got.Reason)
	assert.Equal(t, "eth0 is down", got.Message)
	assert.Equal(t, at, got.At)
	assert.Equal(t, "node-1", got.Node)
	assert.NotEmpty(t, got.NMAVersion)
}

func TestWorker_GuardHeldSkipsCollectionAndFreesCooldown(t *testing.T) {
	counts := map[string]int{}
	guard := &captureguard.Guard{}
	ch := make(chan manager.CaptureRequest)
	c := bucketClient(t)
	w := NewWorker(ch, c, nil, guard, "node-1", countingLogger(counts))
	collectFn, calls := stubCollect(0)
	w.collect = collectFn

	// The NodeDiagnostic controller holds the guard.
	require.True(t, guard.TryAcquire())

	req := manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"}
	w.handle(context.Background(), req)

	assert.Equal(t, 0, *calls, "collect must not run while the guard is held elsewhere")
	assert.Equal(t, 1, counts[msgSkipGuard], "a guard conflict must log the skip message")

	// The skipped request must not have consumed the cooldown.
	guard.Release()
	w.handle(context.Background(), req)
	assert.Equal(t, 1, *calls, "a later request must be collected once the guard is free")
	assert.Equal(t, 0, counts[msgSkip], "the earlier guard skip must not have consumed the cooldown")
}

func TestWorker_GuardFreedAfterHandleReturns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		collect collectFunc
	}{
		{
			name: "success",
			collect: func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error) {
				return Summary{}, nil
			},
		},
		{
			name: "collect error",
			collect: func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error) {
				return Summary{}, errors.New("collect boom")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := map[string]int{}
			guard := &captureguard.Guard{}
			w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, guard, "node-1", countingLogger(counts))
			calls := 0
			w.collect = func(ctx context.Context, cats []v1alpha1.LogCategory, trig Trigger, handle StepFunc) (Summary, error) {
				calls++
				return tc.collect(ctx, cats, trig, handle)
			}

			w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})

			assert.Equal(t, 1, calls, "collect must be called exactly once")
			assert.True(t, guard.TryAcquire(), "the guard must be free once handle returns")
			guard.Release()
		})
	}
}

// The guard must stay held while the collect goroutine runs, even after handle
// times out. The stub ignores ctx, so only the goroutine can explain a held guard.
func TestWorker_GuardHeldUntilCollectReturns(t *testing.T) {
	counts := map[string]int{}
	guard := &captureguard.Guard{}
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, guard, "node-1", countingLogger(counts))
	w.captureTimeout = 10 * time.Millisecond

	release := make(chan struct{})
	returned := make(chan struct{})
	w.collect = func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error) {
		<-release
		close(returned)
		return Summary{}, errors.New("collect boom")
	}

	done := make(chan struct{})
	go func() {
		w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after the capture timeout")
	}

	assert.False(t, guard.TryAcquire(), "the guard must still be held while collection is outstanding")

	close(release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stubbed collect never returned")
	}

	assert.Eventually(t, func() bool {
		return guard.TryAcquire()
	}, 2*time.Second, 10*time.Millisecond, "the guard must be released once collection actually finishes")
}

func TestWorker_CollectErrorIsLoggedNotFatal(t *testing.T) {
	counts := map[string]int{}
	errs := map[string]error{}
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", countingLoggerWithErrors(counts, errs))
	wantErr := errors.New("collect boom")
	w.collect = func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error) {
		return Summary{}, wantErr
	}

	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})
	assert.Equal(t, 0, counts[msgCollected], "a collect error must not log a successful collection")
	assert.Equal(t, 1, counts[msgFailed], "the collect error must be logged")
	assert.ErrorIs(t, errs[msgFailed], wantErr, "the logged error must be the one collect returned")
}

// A low-disk capture is a skip, not a failure, and keeps the cooldown entry.
func TestWorker_CollectLowDiskKeepsCooldown(t *testing.T) {
	counts := map[string]int{}
	errs := map[string]error{}
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", countingLoggerWithErrors(counts, errs))
	calls := 0
	w.collect = func(context.Context, []v1alpha1.LogCategory, Trigger, StepFunc) (Summary, error) {
		calls++
		return Summary{}, fmt.Errorf("checking disk: %w", ErrLowDisk)
	}

	cur := time.Unix(1_000_000, 0)
	w.now = func() time.Time { return cur }
	req := manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"}

	w.handle(context.Background(), req)
	assert.Equal(t, 1, counts[msgSkipDisk], "low disk must log the skip message")
	assert.Equal(t, 0, counts[msgFailed], "low disk is a skip, not a collect failure")
	assert.Equal(t, 0, counts[msgCollected])

	cur = cur.Add(1 * time.Minute)
	w.handle(context.Background(), req)
	assert.Equal(t, 1, calls, "the cooldown entry must survive a low disk skip")
	assert.Equal(t, 1, counts[msgSkip], "the second request must hit the cooldown")
}

// handle must return at captureTimeout when collect is stuck, and collect must
// see its context cancelled.
func TestWorker_CollectTimeoutReturns(t *testing.T) {
	counts := map[string]int{}
	guard := &captureguard.Guard{}
	w := NewWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, guard, "node-1", countingLogger(counts))
	w.captureTimeout = 10 * time.Millisecond

	sawCancel := make(chan struct{})
	w.collect = func(ctx context.Context, _ []v1alpha1.LogCategory, _ Trigger, _ StepFunc) (Summary, error) {
		<-ctx.Done()
		close(sawCancel)
		return Summary{}, ctx.Err()
	}

	done := make(chan struct{})
	go func() {
		w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after the capture timeout")
	}
	assert.Equal(t, 1, counts[msgTimedOut], "a stuck collect must log the timeout message")

	select {
	case <-sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("stubbed collect never saw its context cancelled")
	}
	assert.Eventually(t, func() bool {
		if guard.TryAcquire() {
			guard.Release()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "the guard must be released once the abandoned collect returns")
}
