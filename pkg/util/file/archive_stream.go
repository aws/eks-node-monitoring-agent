package file

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ErrBundleTooLarge fails the stream when the compressed bundle passes the
// size cap, so an oversized capture is never uploaded as if it were complete.
var ErrBundleTooLarge = errors.New("capture bundle exceeds size cap")

const (
	skipReasonOpenFailed = "cannot open"
	skipReasonNotRegular = "not a regular file"
)

// SkippedFile is a file left out of the archive. Path is relative to the
// archived folder, with forward slashes.
type SkippedFile struct {
	Path   string
	Reason string
}

// Stream is a tar.gz of one step folder, produced while it is read.
type Stream struct {
	pr      *io.PipeReader
	written atomic.Int64
	// done closes when the writer goroutine exits. Close never waits on it.
	done chan struct{}

	mu      sync.Mutex
	skipped []SkippedFile
}

// streamOptions is a per-call test seam, so tests can break a file mid-read.
type streamOptions struct {
	wrapReader func(path string, r io.Reader) io.Reader
}

// StreamTarGzip packs dir as a tar.gz that is built as the caller reads, so the
// archive never sits in memory. maxBytes caps the compressed size including
// the gzip footer; zero or less means no cap. Every failure reaches the reader
// as an error, never as a clean EOF, so a truncated archive cannot pass for a
// whole one. The caller must Close the stream.
func StreamTarGzip(ctx context.Context, dir string, maxBytes int64) *Stream {
	return streamTarGzip(ctx, dir, maxBytes, streamOptions{
		wrapReader: func(_ string, r io.Reader) io.Reader { return r },
	})
}

func streamTarGzip(ctx context.Context, dir string, maxBytes int64, opts streamOptions) *Stream {
	pr, pw := io.Pipe()
	s := &Stream{pr: pr, done: make(chan struct{})}

	// Fails the pipe on cancel even while the goroutine is stuck in a file
	// read on a dead mount, so a reader waiting in Read is not left hanging.
	stop := context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })

	go func() {
		defer close(s.done)
		err := s.write(ctx, pw, dir, maxBytes, opts)
		stop()
		if err != nil {
			// Never pw.Close() here: it looks like a normal EOF to the
			// reader, which would then finish a truncated upload.
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	return s
}

// Read returns the next bytes of the archive.
func (s *Stream) Read(p []byte) (int, error) { return s.pr.Read(p) }

// Close stops the stream without waiting for the writer goroutine: a read
// stuck on a dead mount must not hang the caller. The goroutine exits, closing
// its open file, once that read returns.
func (s *Stream) Close() error { return s.pr.Close() }

// Written is the number of compressed bytes handed to the reader so far.
func (s *Stream) Written() int64 { return s.written.Load() }

// Skipped lists the files left out. It is complete once the stream has ended.
func (s *Stream) Skipped() []SkippedFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SkippedFile(nil), s.skipped...)
}

func (s *Stream) skip(rel, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = append(s.skipped, SkippedFile{Path: rel, Reason: reason})
}

// write closes the tar and gzip writers only on success, never deferred:
// closing them on a failure would flush a valid-looking trailer and footer.
func (s *Stream) write(ctx context.Context, w io.Writer, dir string, maxBytes int64, opts streamOptions) error {
	// Counting after gzip bounds what the uploader would actually receive.
	gw := gzip.NewWriter(&countingWriter{w: w, written: &s.written, maxBytes: maxBytes})
	tw := tar.NewWriter(gw)

	// WalkDir visits entries in lexical order, so the archive is deterministic.
	err := filepath.WalkDir(dir, func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, filePath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		switch {
		case d.IsDir():
			return addDir(tw, rel, d)
		case !d.Type().IsRegular():
			// Not opened at all: opening a named pipe would block.
			s.skip(rel, skipReasonNotRegular)
			return nil
		}
		return s.addFile(ctx, tw, filePath, rel, opts)
	})
	if err != nil {
		return err
	}
	// tar before gzip, so the gzip footer counts against the cap too.
	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

func addDir(tw *tar.Writer, rel string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	hdr, err := streamHeader(info, rel+"/")
	if err != nil {
		return err
	}
	return tw.WriteHeader(hdr)
}

// addFile skips and records a file that cannot be opened or statted. Once the
// header is written, a read failure or short file fails the whole archive,
// because the header already promised that many bytes.
func (s *Stream) addFile(ctx context.Context, tw *tar.Writer, filePath, rel string, opts streamOptions) error {
	f, err := os.Open(filePath)
	if err != nil {
		s.skip(rel, skipReasonOpenFailed)
		return nil
	}
	defer f.Close()

	// Stat the opened file, not the walk's entry, so the header size comes
	// from the file we actually read. This also skips a file whose type
	// changed after the walk. It cannot save us from a named pipe swapped
	// in after the walk, because os.Open would already have blocked. The
	// folder is private to the agent, so that is not expected.
	info, err := f.Stat()
	if err != nil {
		s.skip(rel, skipReasonOpenFailed)
		return nil
	}
	if !info.Mode().IsRegular() {
		s.skip(rel, skipReasonNotRegular)
		return nil
	}
	hdr, err := streamHeader(info, rel)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	// Copy exactly the promised size: a file that grew cannot corrupt the tar.
	r := &ctxReader{ctx: ctx, r: opts.wrapReader(filePath, f)}
	if _, err := io.CopyN(tw, r, hdr.Size); err != nil {
		// Fail the whole archive once a header is out: a short copy would
		// corrupt the tar, and padding would hide the loss. To pad or skip
		// instead, change only this return.
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("archive %q: %w", rel, err)
	}
	return nil
}

// streamHeader clears the owner names, which only mean something on this node.
func streamHeader(info fs.FileInfo, name string) (*tar.Header, error) {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return nil, err
	}
	hdr.Name = name
	hdr.Uname, hdr.Gname = "", ""
	return hdr, nil
}

// ctxReader stops a copy between reads once ctx is done, so a cancelled
// capture does not finish a large file first.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// countingWriter checks the cap before each write, so no byte past it ever
// reaches the uploader.
type countingWriter struct {
	w       io.Writer
	written *atomic.Int64
	// maxBytes is the compressed size cap; zero or less means no cap.
	maxBytes int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.maxBytes > 0 && c.written.Load()+int64(len(p)) > c.maxBytes {
		return 0, ErrBundleTooLarge
	}
	n, err := c.w.Write(p)
	c.written.Add(int64(n))
	return n, err
}
