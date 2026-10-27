package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveEntry struct {
	name string
	dir  bool
	body string
}

// readArchive untars a tar.gz stream and fails the test on any error.
func readArchive(t *testing.T, r io.Reader) []archiveEntry {
	t.Helper()
	gr, err := gzip.NewReader(r)
	require.NoError(t, err)
	tr := tar.NewReader(gr)
	var entries []archiveEntry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		entries = append(entries, archiveEntry{name: hdr.Name, dir: hdr.Typeflag == tar.TypeDir, body: string(body)})
	}
}

// extract writes the entries under dest, as a consumer of the archive would.
func extract(t *testing.T, entries []archiveEntry, dest string) {
	t.Helper()
	for _, e := range entries {
		path := filepath.Join(dest, filepath.FromSlash(e.name))
		if e.dir {
			require.NoError(t, os.MkdirAll(path, 0o755))
			continue
		}
		writeFile(t, path, []byte(e.body))
	}
}

// snapshot maps every path under root to its content; directories end in "/".
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		body, err := os.ReadFile(path)
		got[rel] = string(body)
		return err
	}))
	return got
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// waitDone fails the test if the writer goroutine does not exit.
func waitDone(t *testing.T, s *Stream) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer goroutine did not exit")
	}
}

// within fails fast instead of hanging the suite if fn blocks.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		fn()
	}()
	select {
	case <-finished:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

// openRecorder remembers the files the stream opened, to check they get closed.
// The reader handed to wrapReader is the opened *os.File itself.
type openRecorder struct {
	mu    sync.Mutex
	files []*os.File
}

func (o *openRecorder) record(r io.Reader) {
	if f, ok := r.(*os.File); ok {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.files = append(o.files, f)
	}
}

func (o *openRecorder) wrapReader(_ string, r io.Reader) io.Reader {
	o.record(r)
	return r
}

func (o *openRecorder) requireAllClosed(t *testing.T) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	require.NotEmpty(t, o.files)
	for _, f := range o.files {
		_, err := f.Stat()
		assert.ErrorIs(t, err, os.ErrClosed, f.Name())
	}
}

// hooks applies a reader wrapper to the files whose base name matches.
func hooks(name string, wrap func(r io.Reader) io.Reader) streamOptions {
	return streamOptions{
		wrapReader: func(path string, r io.Reader) io.Reader {
			if filepath.Base(path) == name {
				return wrap(r)
			}
			return r
		},
	}
}

func TestStreamTarGzipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"z.txt":            "zulu",
		"a.txt":            "alpha",
		"empty.txt":        "",
		"sub/b.txt":        "bravo",
		"sub/deep/c.txt":   "charlie",
		"sub/deep/d/e.txt": "echo",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), []byte(content))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "empty", "nested"), 0o755))

	s := StreamTarGzip(context.Background(), dir, 0)
	defer s.Close()
	entries := readArchive(t, s)
	waitDone(t, s)

	// WalkDir order is lexical, so the archive order is stable.
	var names []string
	for _, e := range entries {
		names = append(names, e.name)
		// names are relative: the temp path must never leak into the bundle
		assert.NotContains(t, e.name, dir)
		assert.False(t, strings.HasPrefix(e.name, "/"), e.name)
	}
	assert.Equal(t, []string{
		"a.txt",
		"empty.txt",
		"sub/",
		"sub/b.txt",
		"sub/deep/",
		"sub/deep/c.txt",
		"sub/deep/d/",
		"sub/deep/d/e.txt",
		"sub/empty/",
		"sub/empty/nested/",
		"z.txt",
	}, names)

	dest := t.TempDir()
	extract(t, entries, dest)
	assert.Equal(t, snapshot(t, dir), snapshot(t, dest))
	assert.Empty(t, s.Skipped())
}

func TestStreamTarGzipSizeCap(t *testing.T) {
	dir := t.TempDir()
	// random data does not compress, so the archive is about as big as the input
	writeFile(t, filepath.Join(dir, "random.bin"), randomBytes(t, 256<<10))

	full, err := io.ReadAll(StreamTarGzip(context.Background(), dir, 0))
	require.NoError(t, err)
	size := int64(len(full))

	tests := []struct {
		name     string
		maxBytes int64
		wantErr  bool
	}{
		{name: "far below the archive size", maxBytes: 1024, wantErr: true},
		// the gzip footer is the last write, so one byte short only fails there
		{name: "one byte short of the archive size", maxBytes: size - 1, wantErr: true},
		{name: "exactly the archive size", maxBytes: size},
		{name: "zero means no cap", maxBytes: 0},
		{name: "negative means no cap", maxBytes: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := StreamTarGzip(context.Background(), dir, tc.maxBytes)
			defer s.Close()
			got, err := io.ReadAll(s)
			waitDone(t, s)

			if !tc.wantErr {
				require.NoError(t, err)
				assert.Equal(t, full, got)
				return
			}
			// a clean EOF here would let an uploader finish a truncated object
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBundleTooLarge)
			assert.LessOrEqual(t, int64(len(got)), tc.maxBytes, "nothing over the cap may be written")
			assert.LessOrEqual(t, s.Written(), tc.maxBytes)
		})
	}
}

func TestStreamTarGzipSkipsUnopenableFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), []byte("alpha"))
	writeFile(t, filepath.Join(dir, "locked.txt"), []byte("secret"))
	writeFile(t, filepath.Join(dir, "sub", "locked.log"), []byte("secret"))
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), []byte("charlie"))

	// Root ignores file modes, so the open only fails for a normal user.
	if os.Geteuid() == 0 {
		t.Skip("root can open any file")
	}
	for _, name := range []string{"locked.txt", "sub/locked.log"} {
		require.NoError(t, os.Chmod(filepath.Join(dir, filepath.FromSlash(name)), 0))
	}
	s := StreamTarGzip(context.Background(), dir, 0)
	defer s.Close()
	entries := readArchive(t, s)
	waitDone(t, s)

	assert.Equal(t, []archiveEntry{
		{name: "a.txt", body: "alpha"},
		{name: "sub/", dir: true},
		{name: "sub/c.txt", body: "charlie"},
	}, entries)
	skipped := s.Skipped()
	require.Len(t, skipped, 2)
	assert.Equal(t, []string{"locked.txt", "sub/locked.log"}, []string{skipped[0].Path, skipped[1].Path})
	for _, f := range skipped {
		assert.NotEmpty(t, f.Reason, f.Path)
	}
}

func TestStreamTarGzipFailsWhenFileBreaksAfterHeader(t *testing.T) {
	const size = 100 << 10
	errBoom := errors.New("boom")
	tests := []struct {
		name string
		wrap func(r io.Reader) io.Reader
		want error
	}{
		{
			name: "read error halfway",
			wrap: func(r io.Reader) io.Reader {
				return io.MultiReader(io.LimitReader(r, size/2), &errReader{err: errBoom})
			},
			want: errBoom,
		},
		{
			// the file shrank after the stat, so it ends before its header says
			name: "file shorter than its header",
			wrap: func(r io.Reader) io.Reader { return io.LimitReader(r, size/2) },
			want: io.ErrUnexpectedEOF,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "a.txt"), []byte("alpha"))
			writeFile(t, filepath.Join(dir, "big.bin"), randomBytes(t, size))

			s := streamTarGzip(context.Background(), dir, 0, hooks("big.bin", tc.wrap))
			defer s.Close()
			_, err := io.ReadAll(s)
			waitDone(t, s)

			// ReadAll treats only a bare io.EOF as success
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
			assert.NotErrorIs(t, err, io.EOF)
			assert.Empty(t, s.Skipped())
		})
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

func TestStreamTarGzipContextCancelled(t *testing.T) {
	tests := []struct {
		name string
		// run cancels the context at the point under test and returns the
		// error the reader ends with
		run func(t *testing.T, s *Stream, cancel context.CancelFunc) error
	}{
		{
			name: "cancelled before the first read",
			run: func(t *testing.T, s *Stream, cancel context.CancelFunc) error {
				cancel()
				_, err := io.ReadAll(s)
				return err
			},
		},
		{
			name: "cancelled while copying a large file",
			run: func(t *testing.T, s *Stream, cancel context.CancelFunc) error {
				_, err := io.ReadFull(s, make([]byte, 1024))
				require.NoError(t, err)
				cancel()
				_, err = io.ReadAll(s)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"a.bin", "b.bin"} {
				writeFile(t, filepath.Join(dir, name), randomBytes(t, 4<<20))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := StreamTarGzip(ctx, dir, 0)
			defer s.Close()

			err := tc.run(t, s, cancel)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			waitDone(t, s)
		})
	}
}

// blockingReader blocks its first Read until release closes, like a read stuck
// on a dead mount.
type blockingReader struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingReader) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return 0, io.EOF
}

func TestStreamTarGzipEarlyClose(t *testing.T) {
	t.Run("writer waiting on the reader", func(t *testing.T) {
		dir := t.TempDir()
		// far more than gzip and the pipe can hold, so the goroutine is still
		// copying (file open) when the reader stops
		writeFile(t, filepath.Join(dir, "big.bin"), randomBytes(t, 8<<20))
		rec := &openRecorder{}
		s := streamTarGzip(context.Background(), dir, 0, streamOptions{wrapReader: rec.wrapReader})

		_, err := io.ReadFull(s, make([]byte, 16))
		require.NoError(t, err)
		within(t, 2*time.Second, "Close", func() { assert.NoError(t, s.Close()) })

		waitDone(t, s)
		rec.requireAllClosed(t)
		_, err = s.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.ErrClosedPipe)
	})

	t.Run("pending file read never returns", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "stuck.bin"), []byte("data"))
		rec := &openRecorder{}
		block := &blockingReader{entered: make(chan struct{}), release: make(chan struct{})}
		s := streamTarGzip(context.Background(), dir, 0, streamOptions{
			wrapReader: func(_ string, r io.Reader) io.Reader {
				rec.record(r)
				return block
			},
		})
		// the header must be consumed before the goroutine reaches the file
		go io.Copy(io.Discard, s)
		within(t, 5*time.Second, "the file read starting", func() { <-block.entered })

		within(t, 2*time.Second, "Close", func() { assert.NoError(t, s.Close()) })
		select {
		case <-s.done:
			t.Fatal("goroutine exited while its read was still pending")
		default:
		}

		close(block.release)
		waitDone(t, s)
		rec.requireAllClosed(t)
	})
}

func TestStreamTarGzipEmptyDir(t *testing.T) {
	s := StreamTarGzip(context.Background(), t.TempDir(), 0)
	defer s.Close()

	assert.Empty(t, readArchive(t, s))
	waitDone(t, s)
}

func TestStreamTarGzipWrittenMatchesBytesRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "random.bin"), randomBytes(t, 100<<10))
	writeFile(t, filepath.Join(dir, "sub", "text.txt"), []byte(strings.Repeat("log line\n", 1000)))

	s := StreamTarGzip(context.Background(), dir, 0)
	defer s.Close()
	data, err := io.ReadAll(s)

	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), s.Written())
}

// countingReader counts the source bytes the archive has pulled so far.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func TestStreamTarGzipLargeFileStaysOutOfMemory(t *testing.T) {
	const size = 64 << 20
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "zeros.bin"))
	require.NoError(t, err)
	// a sparse file reads back as zeros without writing 64 MiB to disk
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())

	var sourceRead atomic.Int64
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	s := streamTarGzip(context.Background(), dir, 0, streamOptions{
		wrapReader: func(_ string, r io.Reader) io.Reader {
			return &countingReader{r: r, n: &sourceRead}
		},
	})
	defer s.Close()

	var consumed int64
	buf := make([]byte, 512)
	var readAtFirstKiB int64 = -1
	for {
		n, err := s.Read(buf)
		consumed += int64(n)
		if readAtFirstKiB < 0 && consumed >= 1<<10 {
			readAtFirstKiB = sourceRead.Load()
		}
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	runtime.ReadMemStats(&after)

	// The real proof of streaming: buffering would read the whole source before
	// the consumer's first KiB (streaming measured ~640 KiB), so size/2 has margin.
	require.GreaterOrEqual(t, readAtFirstKiB, int64(0))
	assert.Less(t, readAtFirstKiB, int64(size/2))
	// TotalAlloc only grows, so it does not depend on when the GC runs.
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size/4))
}

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes are not supported here: %v", err)
	}
}

// readAllWithin bounds the read so a regression that blocks on a named pipe
// fails fast instead of hanging the suite.
func readAllWithin(t *testing.T, s *Stream) ([]byte, error) {
	t.Helper()
	var data []byte
	var err error
	within(t, 10*time.Second, "reading the stream", func() { data, err = io.ReadAll(s) })
	return data, err
}

func TestStreamTarGzipSkipsSpecialFiles(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T, dir, path string)
	}{
		{
			name:   "named pipe",
			create: func(t *testing.T, _, path string) { mkfifo(t, path) },
		},
		{
			name: "symlink to a file",
			create: func(t *testing.T, dir, path string) {
				require.NoError(t, os.Symlink(filepath.Join(dir, "keep.txt"), path))
			},
		},
		{
			name: "symlink to a directory",
			create: func(t *testing.T, dir, path string) {
				require.NoError(t, os.Symlink(filepath.Join(dir, "sub"), path))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "keep.txt"), []byte("kept"))
			writeFile(t, filepath.Join(dir, "sub", "inner.txt"), []byte("inner"))
			// both at the top level and nested
			tc.create(t, dir, filepath.Join(dir, "special"))
			tc.create(t, dir, filepath.Join(dir, "sub", "special"))

			s := StreamTarGzip(context.Background(), dir, 0)
			defer s.Close()
			data, err := readAllWithin(t, s)
			require.NoError(t, err)
			waitDone(t, s)

			assert.Equal(t, []archiveEntry{
				{name: "keep.txt", body: "kept"},
				{name: "sub/", dir: true},
				{name: "sub/inner.txt", body: "inner"},
			}, readArchive(t, bytes.NewReader(data)))
			skipped := s.Skipped()
			require.Len(t, skipped, 2)
			assert.Equal(t, []string{"special", "sub/special"}, []string{skipped[0].Path, skipped[1].Path})
			for _, f := range skipped {
				assert.NotEmpty(t, f.Reason, f.Path)
			}
		})
	}
}
