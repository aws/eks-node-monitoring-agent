package observer

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failing reopen must leave fileReader nil, or readAll keeps reading the
// closed file instead of returning io.EOF and the work loop spins.
//
// Internal test because fileObserver is unexported. The file is empty so
// nothing here reaches Broadcast.
func TestFileObserver_ReopenFailureClearsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	o := &fileObserver{path: path}

	require.NoError(t, o.openReader())
	require.NotNil(t, o.fileReader)

	require.NoError(t, os.Remove(path))

	err := o.openReader()
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.True(t, o.fileReader == nil, "a failed reopen left the reader on the closed file")
	require.ErrorIs(t, o.readAll(), io.EOF)
}
