package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
