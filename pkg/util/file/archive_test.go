package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGzipFile_Success(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "test.pcap")
	require.NoError(t, os.WriteFile(srcPath, []byte("test pcap data"), 0644))

	err := GzipFile(srcPath)
	require.NoError(t, err)

	// Original should be deleted
	_, statErr := os.Stat(srcPath)
	assert.True(t, os.IsNotExist(statErr), "original file should be deleted")

	// .gz should exist
	gzPath := srcPath + ".gz"
	_, statErr = os.Stat(gzPath)
	assert.NoError(t, statErr, ".gz file should exist")
}
