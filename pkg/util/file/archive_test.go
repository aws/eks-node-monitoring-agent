package file_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/util/file"
	"github.com/stretchr/testify/assert"
)

func TestTarValid(t *testing.T) {
	expectedFileName := "example.txt"
	expectedBytes := []byte("example")

	logDir := t.TempDir()

	if err := os.WriteFile(path.Join(logDir, expectedFileName), expectedBytes, 0644); err != nil {
		t.Fatal(err)
	}

	archiveReader, err := file.TarGzipDir(logDir)
	if err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(archiveReader)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)

	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}

	fileBytes := make([]byte, len(expectedBytes))
	if _, err := reader.Read(fileBytes); err != nil && err != io.EOF {
		t.Fatal(err)
	}

	assert.Equal(t, header.Name, expectedFileName)
	assert.Equal(t, fileBytes, expectedBytes)

	if err := os.RemoveAll(logDir); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureParentExists(t *testing.T) {
	assert.NoError(t, file.EnsureParentExists("/tmp/foo", 0o755))
	assert.Error(t, file.EnsureParentExists("/non-existent-path/foo", 0o755))
}
