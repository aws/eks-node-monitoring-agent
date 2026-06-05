package packet_capture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindGzipFiles_MixedFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap00001.gz"), []byte("gz1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap00002.gz"), []byte("gz2"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap"), []byte("pcap"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("txt"), 0644))

	files, err := findGzipFiles(dir)
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Equal(t, filepath.Join(dir, "capture.pcap00001.gz"), files[0])
	assert.Equal(t, filepath.Join(dir, "capture.pcap00002.gz"), files[1])
}

func TestFindGzipFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	files, err := findGzipFiles(dir)
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestFindNonGzipFiles_MixedFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap"), []byte("pcap"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap00001"), []byte("pcap1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capture.pcap00001.gz"), []byte("gz"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.log"), []byte("log"), 0644))

	files, err := findNonGzipFiles(dir, "capture.pcap")
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Equal(t, filepath.Join(dir, "capture.pcap"), files[0])
	assert.Equal(t, filepath.Join(dir, "capture.pcap00001"), files[1])
}

func TestDeleteCorrespondingPcapFile_Exists(t *testing.T) {
	dir := t.TempDir()
	pcapPath := filepath.Join(dir, "capture.pcap00001")
	gzPath := pcapPath + ".gz"
	require.NoError(t, os.WriteFile(pcapPath, []byte("pcap"), 0644))
	require.NoError(t, os.WriteFile(gzPath, []byte("gz"), 0644))

	err := deleteCorrespondingPcapFile(gzPath)
	require.NoError(t, err)

	_, statErr := os.Stat(pcapPath)
	assert.True(t, os.IsNotExist(statErr), "pcap file should be deleted")
}

func TestDeleteCorrespondingPcapFile_NotExists(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "capture.pcap00001.gz")
	require.NoError(t, os.WriteFile(gzPath, []byte("gz"), 0644))

	err := deleteCorrespondingPcapFile(gzPath)
	assert.NoError(t, err, "should return nil when pcap file doesn't exist")
}

func TestDeleteCorrespondingPcapFile_NonGzInput(t *testing.T) {
	err := deleteCorrespondingPcapFile("/tmp/capture.pcap")
	assert.Error(t, err, "should return error for non-.gz input")
	assert.Contains(t, err.Error(), "expected .gz file")
}
