package autocapture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/captureguard"
	fileutil "github.com/aws/eks-node-monitoring-agent/pkg/util/file"
)

// fakeUploader records each call. It keeps every body so a test can read it
// after handle returns: a closed Stream then fails with io.ErrClosedPipe,
// which shows the worker closed it without adding a hook to Stream.
type fakeUploader struct {
	fn     func(key string, body io.Reader) error
	keys   []string
	bodies []io.Reader
}

func (f *fakeUploader) Upload(_ context.Context, bucket, key string, body io.Reader) error {
	f.keys = append(f.keys, key)
	f.bodies = append(f.bodies, body)
	if f.fn != nil {
		return f.fn(key, body)
	}
	return nil
}

func (f *fakeUploader) requireAllClosed(t *testing.T) {
	t.Helper()
	for i, body := range f.bodies {
		_, err := body.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.ErrClosedPipe, "stream for %s must be closed", f.keys[i])
	}
}

// readAll is an Upload that consumes the body, as S3 would.
func readAll(_ string, body io.Reader) error {
	_, err := io.Copy(io.Discard, body)
	return err
}

type logEntry struct {
	msg string
	err error
	kv  map[string]any
}

// entryLogger keeps every message with its fields.
func entryLogger(entries *[]logEntry) logr.Logger {
	return logr.New(&entrySink{entries: entries})
}

type entrySink struct{ entries *[]logEntry }

func (s *entrySink) Init(logr.RuntimeInfo)          {}
func (s *entrySink) Enabled(int) bool               { return true }
func (s *entrySink) WithValues(...any) logr.LogSink { return s }
func (s *entrySink) WithName(string) logr.LogSink   { return s }
func (s *entrySink) Info(_ int, msg string, kv ...any) {
	s.Error(nil, msg, kv...)
}
func (s *entrySink) Error(err error, msg string, kv ...any) {
	fields := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		fields[kv[i].(string)] = kv[i+1]
	}
	*s.entries = append(*s.entries, logEntry{msg: msg, err: err, kv: fields})
}

func findLogs(entries []logEntry, msg string) (out []logEntry) {
	for _, e := range entries {
		if e.msg == msg {
			out = append(out, e)
		}
	}
	return out
}

func keySuffixes(keys []string) (out []string) {
	for _, k := range keys {
		out = append(out, k[strings.LastIndex(k, "/")+1:])
	}
	return out
}

const (
	msgStoppedEarly  = "capture stopped early"
	msgDenied        = "upload denied, stopping uploads"
	msgUnavailable   = "upload unavailable"
	msgStepSizeCap   = "step skipped: size cap"
	msgStepUploadErr = "failed to upload step"
)

func TestStepKey(t *testing.T) {
	// 14:40:12 at UTC-7 is 21:40:12 UTC.
	at := time.Date(2026, 10, 28, 14, 40, 12, 0, time.FixedZone("PDT", -7*60*60))
	node := "ip-1-2-3-4.us-west-2.compute.internal"

	for _, tc := range []struct{ step, want string }{
		{"Base/CommonLogs", "node-captures/ip-1-2-3-4.us-west-2.compute.internal/20261028T214012Z-KernelReady/Base.CommonLogs.tar.gz"},
		{triggerStepName, "node-captures/ip-1-2-3-4.us-west-2.compute.internal/20261028T214012Z-KernelReady/capture-trigger.tar.gz"},
	} {
		assert.Equal(t, tc.want, stepKey(node, at, "KernelReady", tc.step))
	}
}

// writeStepDir makes a folder with one file and a fixed mtime, so its
// compressed size is the same every time it is streamed.
func writeStepDir(t *testing.T, content []byte) (dir string, compressed int64) {
	t.Helper()
	dir = t.TempDir()
	file := filepath.Join(dir, "log")
	require.NoError(t, os.WriteFile(file, content, 0644))
	require.NoError(t, os.Chtimes(file, time.Unix(1_000_000, 0), time.Unix(1_000_000, 0)))

	stream := fileutil.StreamTarGzip(context.Background(), dir, 0)
	defer stream.Close()
	n, err := io.Copy(io.Discard, stream)
	require.NoError(t, err)
	return dir, n
}

// The steps' compressed sizes are measured first, so each budget sits exactly
// on, or one byte below, what a step needs.
func TestWorker_UploadBudget(t *testing.T) {
	random := make([]byte, 64<<10) // incompressible, so s2 is far larger than s1+s3
	rand.New(rand.NewSource(1)).Read(random)
	dir1, w1 := writeStepDir(t, bytes.Repeat([]byte("a"), 100))
	dir2, w2 := writeStepDir(t, random)
	dir3, w3 := writeStepDir(t, bytes.Repeat([]byte("c"), 200))
	require.Greater(t, w2, w1+w3)
	steps := []Step{{Name: "Base/s1", Dir: dir1}, {Name: "Base/s2", Dir: dir2}, {Name: "Base/s3", Dir: dir3}}

	errBoom := errors.New("upload boom")
	for _, tc := range []struct {
		name          string
		budget        int64
		failS1        bool
		wantCalls     []string
		wantUploaded  []string
		wantUsed      int64
		wantFailed    int
		wantSizeCap   []string
		wantSizeCapLn int // "step skipped: size cap" lines, only for streams that hit the cap
	}{
		{
			// s3 fits the remainder exactly, so the cap is budget minus s1's Written().
			name:          "too large step skipped, next fits the exact remainder",
			budget:        w1 + w3,
			wantCalls:     []string{"Base.s1.tar.gz", "Base.s2.tar.gz", "Base.s3.tar.gz"},
			wantUploaded:  []string{"Base.s1.tar.gz", "Base.s3.tar.gz"},
			wantUsed:      w1 + w3,
			wantSizeCap:   []string{"Base/s2: size cap"},
			wantSizeCapLn: 1,
		},
		{
			name:          "one byte short skips the last step too",
			budget:        w1 + w3 - 1,
			wantCalls:     []string{"Base.s1.tar.gz", "Base.s2.tar.gz", "Base.s3.tar.gz"},
			wantUploaded:  []string{"Base.s1.tar.gz"},
			wantUsed:      w1,
			wantSizeCap:   []string{"Base/s2: size cap", "Base/s3: size cap"},
			wantSizeCapLn: 2,
		},
		{
			// remaining is 0 after s1, which StreamTarGzip would read as no cap.
			name:         "used up budget never reaches the stream",
			budget:       w1,
			wantCalls:    []string{"Base.s1.tar.gz"},
			wantUploaded: []string{"Base.s1.tar.gz"},
			wantUsed:     w1,
			wantSizeCap:  []string{"Base/s2: size cap", "Base/s3: size cap"},
		},
		{
			// Had s1 counted, s2 would not fit.
			name:         "failed upload does not use the budget",
			budget:       w2,
			failS1:       true,
			wantCalls:    []string{"Base.s1.tar.gz", "Base.s2.tar.gz"},
			wantUploaded: []string{"Base.s2.tar.gz"},
			wantUsed:     w2,
			wantFailed:   1,
			wantSizeCap:  []string{"Base/s3: size cap"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entries []logEntry
			var uploaded []string
			up := &fakeUploader{fn: func(key string, body io.Reader) error {
				if err := readAll(key, body); err != nil {
					return err
				}
				if tc.failS1 && strings.HasSuffix(key, "Base.s1.tar.gz") {
					return errBoom
				}
				uploaded = append(uploaded, key)
				return nil
			}}
			w := newTestWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", up, entryLogger(&entries))
			w.captureBudget = tc.budget
			// Mimics collectSteps' HandleErrors count; these folders are the test's own.
			w.collect = func(ctx context.Context, _ []v1alpha1.LogCategory, _ Trigger, handle StepFunc) (Summary, error) {
				var s Summary
				for _, step := range steps {
					if err := handle(ctx, step); err != nil {
						s.HandleErrors++
					}
				}
				return s, nil
			}

			w.handle(context.Background(), manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"})

			assert.Equal(t, tc.wantCalls, keySuffixes(up.keys))
			assert.Equal(t, tc.wantUploaded, keySuffixes(uploaded))
			up.requireAllClosed(t)

			done := findLogs(entries, msgCollected)
			require.Len(t, done, 1)
			assert.Equal(t, tc.wantUsed, done[0].kv["uploadedBytes"])
			assert.Equal(t, len(tc.wantUploaded), done[0].kv["uploaded"])
			assert.Equal(t, tc.wantFailed, done[0].kv["failedUploads"])
			assert.Equal(t, len(tc.wantSizeCap), done[0].kv["skippedCollectors"])
			assert.Len(t, findLogs(entries, msgStepSizeCap), tc.wantSizeCapLn)
			assert.Len(t, findLogs(entries, msgStepUploadErr), tc.wantFailed)

			early := findLogs(entries, msgStoppedEarly)
			require.Len(t, early, 1)
			assert.Equal(t, tc.wantSizeCap, early[0].kv["skipped"])
		})
	}
}

// These run the real collectSteps, so its removal of each folder is what is
// checked, not a stand-in.
func TestWorker_UploadsEachStep(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "secret body"}
	errBoom := errors.New("upload boom")
	all := []string{"capture-trigger.tar.gz", "Base.alpha.tar.gz", "Base.bravo.tar.gz"}

	for _, tc := range []struct {
		name         string
		failOn       string
		failWith     error
		lowDiskAfter int // freeDisk calls that pass; 0 means never low
		wantCalls    []string
		wantRan      int // collectors that ran
		wantUploaded int
		wantFailed   int
		wantDenied   int
		wantEarly    []string
	}{
		{
			name:         "every step uploads",
			wantCalls:    all,
			wantRan:      2,
			wantUploaded: 3,
		},
		{
			name:       "access denied stops the capture",
			failOn:     "capture-trigger.tar.gz",
			failWith:   denied,
			wantCalls:  all[:1],
			wantFailed: 1,
			wantDenied: 1,
			wantEarly:  []string{"Base/alpha: upload denied", "Base/bravo: upload denied"},
		},
		{
			name:         "generic error loses only its step",
			failOn:       "capture-trigger.tar.gz",
			failWith:     errBoom,
			wantCalls:    all,
			wantRan:      2,
			wantUploaded: 2,
			wantFailed:   1,
		},
		{
			// The start check and the check before alpha pass.
			name:         "low disk mid-capture names the skipped collectors",
			lowDiskAfter: 2,
			wantCalls:    all[:2],
			wantRan:      1,
			wantUploaded: 2,
			wantEarly:    []string{"Base/bravo: low disk"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := 0
			write := func(acc *collect.Accessor) error {
				ran++
				return acc.WriteOutput("out.txt", []byte("collected"))
			}
			deps, tmp := testDeps(t, map[v1alpha1.LogCategory][]collect.Collector{
				v1alpha1.LogCategoryBase: {&alpha{fakeCollector{write}}, &bravo{fakeCollector{write}}},
			})
			calls := 0
			deps.freeDisk = func(string) (uint64, uint64, error) {
				calls++
				if tc.lowDiskAfter > 0 && calls > tc.lowDiskAfter {
					return testLimit - 1, testTotal, nil
				}
				return testTotal, testTotal, nil
			}

			var entries []logEntry
			up := &fakeUploader{fn: func(key string, body io.Reader) error {
				folders, err := os.ReadDir(tmp)
				require.NoError(t, err)
				assert.Len(t, folders, 1, "the step folder must exist while Upload runs")
				if strings.HasSuffix(key, tc.failOn) && tc.failWith != nil {
					return tc.failWith
				}
				return readAll(key, body)
			}}
			w := newTestWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", up, entryLogger(&entries))
			w.collect = func(ctx context.Context, categories []v1alpha1.LogCategory, trigger Trigger, handle StepFunc) (Summary, error) {
				return collectSteps(ctx, &config.RuntimeContext{}, categories, trigger, handle, deps)
			}

			w.handle(context.Background(), manager.CaptureRequest{Condition: "KernelReady", Reason: "Panic"})

			assert.Equal(t, tc.wantCalls, keySuffixes(up.keys))
			assert.Equal(t, tc.wantRan, ran)
			assert.Empty(t, findLogs(entries, msgTimedOut))
			assert.Empty(t, findLogs(entries, msgFailed))
			for _, key := range up.keys {
				assert.True(t, strings.HasPrefix(key, "node-captures/node-1/"), key)
			}
			up.requireAllClosed(t)
			requireEmptyDir(t, tmp)

			done := findLogs(entries, msgCollected)
			require.Len(t, done, 1)
			assert.Equal(t, tc.wantUploaded, done[0].kv["uploaded"])
			assert.Equal(t, tc.wantFailed, done[0].kv["failedUploads"])

			deniedLogs := findLogs(entries, msgDenied)
			require.Len(t, deniedLogs, tc.wantDenied, "the denied error must be logged once")
			for _, e := range deniedLogs {
				assert.Equal(t, "my-bucket", e.kv["bucket"])
				assert.NoError(t, e.err, "the error body must not be logged")
			}

			early := findLogs(entries, msgStoppedEarly)
			if tc.wantEarly == nil {
				assert.Empty(t, early)
				return
			}
			require.Len(t, early, 1)
			assert.Equal(t, tc.wantEarly, early[0].kv["skipped"])
		})
	}
}

// Two captures for one condition, with the uploader built on first need.
func TestWorker_BuildsUploaderOnFirstCapture(t *testing.T) {
	errIMDS := errors.New("imds unreachable")
	for _, tc := range []struct {
		name            string
		buildErrs       []error // returned by successive factory calls
		advance         time.Duration
		wantBuilds      int
		wantUnavailable int
		wantCollects    int
	}{
		{
			// No clock advance: only an unrecorded cooldown lets the retry collect.
			name:            "failed build is retried without a cooldown",
			buildErrs:       []error{errIMDS, nil},
			wantBuilds:      2,
			wantUnavailable: 1,
			wantCollects:    1,
		},
		{
			name:         "built uploader is reused",
			buildErrs:    []error{nil},
			advance:      defaultCooldown,
			wantBuilds:   1,
			wantCollects: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entries []logEntry
			up := &fakeUploader{}
			w := newTestWorker(make(chan manager.CaptureRequest), bucketClient(t), nil, &captureguard.Guard{}, "node-1", up, entryLogger(&entries))
			builds := 0
			w.newUploader = func(context.Context) (objectUploader, error) {
				err := tc.buildErrs[builds]
				builds++
				if err != nil {
					return nil, err
				}
				return up, nil
			}
			collectFn, calls := stubCollect(1)
			w.collect = collectFn
			cur := time.Unix(1_000_000, 0)
			w.now = func() time.Time { return cur }
			req := manager.CaptureRequest{Condition: "NetworkingReady", Reason: "Down"}

			w.handle(context.Background(), req)
			cur = cur.Add(tc.advance)
			w.handle(context.Background(), req)

			assert.Equal(t, tc.wantBuilds, builds)
			assert.Equal(t, tc.wantCollects, *calls)
			assert.Len(t, up.keys, tc.wantCollects, "each collected capture must upload its step")
			assert.Empty(t, findLogs(entries, msgSkip))
			unavailable := findLogs(entries, msgUnavailable)
			require.Len(t, unavailable, tc.wantUnavailable)
			for _, e := range unavailable {
				assert.ErrorIs(t, e.err, errIMDS, "the build error must be logged at error level")
			}
		})
	}
}
