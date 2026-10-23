package autocapture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
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

type countingSink struct {
	counts map[string]int
}

func (s *countingSink) Init(logr.RuntimeInfo)                 {}
func (s *countingSink) Enabled(int) bool                      { return true }
func (s *countingSink) WithValues(...any) logr.LogSink        { return s }
func (s *countingSink) WithName(string) logr.LogSink          { return s }
func (s *countingSink) Error(error, string, ...any)           {}
func (s *countingSink) Info(level int, msg string, kv ...any) { s.counts[msg]++ }

const (
	msgOff     = "auto log capture is off: no NodeDiagnostic sets spec.autoLogCapture.bucket"
	msgRequest = "capture requested"
)

func TestWorker_NoBucketLogsOffOnce(t *testing.T) {
	counts := map[string]int{}
	ch := make(chan manager.CaptureRequest, 2)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	w := NewWorker(ch, c, countingLogger(counts))

	// Two requests with no bucket configured: the "off" message logs once.
	req := manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down", At: time.Now()}
	w.handle(context.Background(), req)
	w.handle(context.Background(), req)

	assert.Equal(t, 1, counts[msgOff], "off message must be logged once for repeated requests")
	assert.Equal(t, 0, counts[msgRequest])
}

func TestWorker_BucketHandlesRequest(t *testing.T) {
	counts := map[string]int{}
	ch := make(chan manager.CaptureRequest, 1)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(autoLogDiagnostic("alpha", "my-bucket", time.Unix(100, 0))).Build()
	w := NewWorker(ch, c, countingLogger(counts))

	w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down", At: time.Now()})

	assert.Equal(t, 1, counts[msgRequest], "a configured bucket must produce a capture-requested log")
	assert.Equal(t, 0, counts[msgOff])
}

func TestWorker_StartReturnsNilOnCancel(t *testing.T) {
	ch := make(chan manager.CaptureRequest)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	w := NewWorker(ch, c, logr.Discard())

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
