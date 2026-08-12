package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ContextStopsLogger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Nanosecond)
	defer cancel()
	var buffer bytes.Buffer
	loggerOpts := Settings{
		LogInterval: 1 * time.Nanosecond,
	}
	if err := NewDiagnosticLogger(&buffer, loggerOpts).Start(ctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("returned error: %s", err)
		}
	}
	if buffer.Len() == 0 {
		t.Errorf("should have written diagnostic to logger")
	}
}

// Regression test for issue #221: the client is reused across checks, so the
// agent does not open a new connection and repeat the TLS handshake every cycle.
// Reuse is only possible if the response body is released, so this covers the
// missing Close as well.
func TestAPIServerEndpointReusesConnection(t *testing.T) {
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/livez", r.URL.Path)
		assert.Equal(t, "verbose", r.URL.RawQuery)
		fmt.Fprint(w, "livez check passed")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	for i := 0; i < 3; i++ {
		body := testAPIServerEndpoint(server.URL)
		require.Equal(t, "livez check passed", string(body), "call %d", i+1)
	}

	assert.Equal(t, int32(1), newConnections.Load(),
		"the checks should share one connection; a client per call opens one each")
}

func TestAPIServerEndpointUnreachable(t *testing.T) {
	// port 1 is not listening, so this exercises the path where there is no
	// response and therefore no body to close.
	body := testAPIServerEndpoint("https://127.0.0.1:1")
	assert.Contains(t, string(body), "failed to make request endpoint")
}

// newTestLogger builds a logger over an explicit producer set, bypassing the
// host collectors so that blocking behaviour can be exercised directly.
func newTestLogger(w io.Writer, timeout time.Duration, producers ...*producer) diagnosticLogger {
	return diagnosticLogger{
		settings:     Settings{LogInterval: time.Hour, CollectorTimeout: timeout},
		writer:       w,
		producers:    producers,
		sectionBytes: 4096,
	}
}

// blockingProducer never returns, standing in for a collector stuck on
// unresponsive host I/O (a 'df' against a wedged NFS mount, for example).
func blockingProducer(name string, entered chan<- struct{}) *producer {
	return &producer{name: name, fn: func(context.Context) []byte {
		if entered != nil {
			entered <- struct{}{}
		}
		<-make(chan struct{})
		return nil
	}}
}

// Regression test for issue #219: a collector that never returns must cost its
// own section only. Before the fix it stopped the cycle, so every later section
// went missing for the remaining life of the node.
func TestBlockedCollectorDoesNotStopCycle(t *testing.T) {
	var buffer bytes.Buffer
	logger := newTestLogger(&buffer, 50*time.Millisecond,
		blockingProducer("blocked", nil),
		&producer{name: "after", fn: func(context.Context) []byte { return []byte("after-ran") }},
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		logger.RunOnce(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunOnce did not return, the blocked collector stopped the cycle")
	}

	out := buffer.String()
	assert.Contains(t, out, "|blocked\ncollector \"blocked\" timed out after 50ms",
		"the blocked section must report the timeout as its body")
	assert.Contains(t, out, "|after\nafter-ran", "sections after a blocked collector must still be emitted")
}

// A collector still stuck from a previous cycle must not be started again: the
// wedged process cannot be killed, so respawning it accumulates one stuck
// process and goroutine per cycle for the life of the node.
func TestBlockedCollectorIsNotRestarted(t *testing.T) {
	var buffer bytes.Buffer
	entered := make(chan struct{}, 10)
	logger := newTestLogger(&buffer, 50*time.Millisecond, blockingProducer("blocked", entered))

	logger.RunOnce(context.Background())
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("collector was never started")
	}

	// the second cycle must skip the outstanding collector rather than wait for
	// the timeout again.
	buffer.Reset()
	start := time.Now()
	logger.RunOnce(context.Background())

	assert.Empty(t, entered, "outstanding collector must not be started again")
	assert.Less(t, time.Since(start), 50*time.Millisecond, "skipping must not wait for the timeout")
	assert.Contains(t, buffer.String(), "has not returned after", "the section must report the collector as outstanding")
}

// A collector that returns normally is re-run every cycle, i.e. the single
// flight guard releases on completion.
func TestHealthyCollectorRunsEveryCycle(t *testing.T) {
	var buffer bytes.Buffer
	var runs atomic.Int32
	logger := newTestLogger(&buffer, time.Minute,
		&producer{name: "healthy", fn: func(context.Context) []byte {
			runs.Add(1)
			return []byte("ok")
		}},
	)

	logger.RunOnce(context.Background())
	logger.RunOnce(context.Background())

	assert.Equal(t, int32(2), runs.Load())
	assert.Equal(t, 2, strings.Count(buffer.String(), "|healthy\nok"))
}

// The collector context is cancelled at the deadline so that a killable child
// process is terminated rather than left running.
func TestCollectorContextIsCancelledAtDeadline(t *testing.T) {
	var buffer bytes.Buffer
	cancelled := make(chan error, 1)
	logger := newTestLogger(&buffer, 50*time.Millisecond,
		&producer{name: "watcher", fn: func(ctx context.Context) []byte {
			<-ctx.Done()
			cancelled <- ctx.Err()
			return nil
		}},
	)

	logger.RunOnce(context.Background())

	select {
	case err := <-cancelled:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(10 * time.Second):
		t.Fatal("collector context was never cancelled")
	}
}

// Shutdown is reported as shutdown rather than as a collector timeout.
func TestShutdownDuringCollection(t *testing.T) {
	var buffer bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := newTestLogger(&buffer, time.Minute, blockingProducer("blocked", nil))
	logger.RunOnce(ctx)

	assert.Contains(t, buffer.String(), "did not finish before shutdown")
}

// RunOnce emits one cycle and returns, which is what makes it usable for the
// final flush on exit; Start would block on its ticker forever.
func TestRunOnceEmitsSingleCycle(t *testing.T) {
	var buffer bytes.Buffer
	logger := newTestLogger(&buffer, time.Minute,
		&producer{name: "one", fn: func(context.Context) []byte { return []byte("body") }},
	)

	logger.RunOnce(context.Background())

	assert.Equal(t, 1, strings.Count(buffer.String(), sectionMarker))
}

func TestNewDiagnosticLoggerDefaults(t *testing.T) {
	logger := NewDiagnosticLogger(io.Discard, Settings{})
	assert.Equal(t, defaultCollectorTimeout, logger.settings.CollectorTimeout)
	assert.Equal(t, 5*time.Minute, logger.settings.LogInterval)
	assert.NotEmpty(t, logger.producers, "producers must be built up front so they can track outstanding runs")
	assert.Positive(t, logger.sectionBytes)

	t.Run("RespectsExplicitTimeout", func(t *testing.T) {
		logger := NewDiagnosticLogger(io.Discard, Settings{CollectorTimeout: time.Second})
		assert.Equal(t, time.Second, logger.settings.CollectorTimeout)
	})
}

func TestUtils(t *testing.T) {
	t.Run("TailX", func(t *testing.T) {
		assert.Equal(t, "World", string(tailx([]byte("Hello, World"), 5)))
		assert.Equal(t, "Hello, World", string(tailx([]byte("Hello, World"), 99)))
		assert.Equal(t, "World", string(tailx([]byte("Hello,\nWorld"), 5)))
		assert.Equal(t, "World", string(tailx([]byte("Hello,\nWorld"), 7)))
		assert.Equal(t, "World\nAgain!", string(tailx([]byte("Hello,\nWorld\nAgain!"), 15)))
		assert.Equal(t, "Hello,\nWorld\nAgain!", string(tailx([]byte("Hello,\nWorld\nAgain!"), 99)))
	})
}

func TestReadFileTail(t *testing.T) {
	dir := t.TempDir()

	t.Run("SmallerThanLimit_ReturnsWholeFile", func(t *testing.T) {
		p := filepath.Join(dir, "small.log")
		content := []byte("line1\nline2\nline3\n")
		require.NoError(t, os.WriteFile(p, content, 0o644))
		assert.Equal(t, content, readFileTail(p, 1024))
	})

	t.Run("LargerThanLimit_ReturnsBoundedTail", func(t *testing.T) {
		p := filepath.Join(dir, "big.log")
		// 1 MiB of filler followed by a known 4 KiB tail; reading with a 4 KiB
		// limit must return exactly the tail and never the whole file.
		filler := bytes.Repeat([]byte("A"), 1<<20)
		tail := bytes.Repeat([]byte("B"), 4096)
		require.NoError(t, os.WriteFile(p, append(filler, tail...), 0o644))

		got := readFileTail(p, 4096)
		assert.Len(t, got, 4096, "must not read more than the limit")
		assert.Equal(t, tail, got, "must return the last n bytes")
	})

	t.Run("MissingFile_ReturnsErrorMessage", func(t *testing.T) {
		got := readFileTail(filepath.Join(dir, "does-not-exist.log"), 1024)
		assert.Contains(t, string(got), "failed to read")
	})
}
