package observer_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

func TestFileObserver_OpenAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	cancel()
	// Use the constructor to create a file observer with a non-existent path.
	// The cancelled context ensures Init returns immediately.
	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeFile]([]resource.Part{resource.Part("foobar")})
	assert.NoError(t, err)
	assert.NoError(t, obs.Init(ctx))
}

func TestFileObserver_WriteTemp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()

	var tmpFilePath = filepath.Join(t.TempDir(), "nma-file-observer-test")

	fileParts := []resource.Part{resource.Part(tmpFilePath)}
	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeFile](fileParts)
	if err != nil {
		t.Fatal(err)
	}

	obsChan := obs.Subscribe()

	go obs.Init(ctx)
	// wait so that the observer starts an fsnotify watcher
	time.Sleep(200 * time.Millisecond)

	tmpFile, err := os.Create(tmpFilePath)
	if err != nil {
		t.Fatal(err)
	}

	const sampleData = "example"

	// wait in case the observer seeks to the end before the file is picked up
	time.Sleep(200 * time.Millisecond)

	if _, err := tmpFile.WriteString(sampleData + "\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-obsChan:
		assert.Equal(t, line, sampleData)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestFileObserver_WriteTempExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()

	tmpFile, err := os.CreateTemp(t.TempDir(), "*")
	if err != nil {
		t.Fatal(err)
	}

	fileParts := []resource.Part{resource.Part(tmpFile.Name())}
	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeFile](fileParts)
	if err != nil {
		t.Fatal(err)
	}

	obsChan := obs.Subscribe()

	go obs.Init(ctx)

	const sampleData = "example"

	// wait in case the observer seeks to the end before the file is picked up
	time.Sleep(200 * time.Millisecond)
	if _, err := tmpFile.WriteString(sampleData + "\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-obsChan:
		assert.Equal(t, line, sampleData)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestFileObserver_RecreateFileTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()

	tmpFile, err := os.CreateTemp(t.TempDir(), "*")
	assert.NoError(t, err)

	fileParts := []resource.Part{resource.Part(tmpFile.Name())}
	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeFile](fileParts)
	assert.NoError(t, err)

	obsChan := obs.Subscribe()

	go obs.Init(ctx)

	const sampleData1 = "example-1"

	// wait in case the observer seeks to the end before the file is picked up
	time.Sleep(200 * time.Millisecond)
	if _, err := tmpFile.WriteString(sampleData1 + "\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-obsChan:
		assert.Equal(t, line, sampleData1)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	assert.NoError(t, tmpFile.Close())
	assert.NoError(t, os.Remove(tmpFile.Name()))

	// recreate the file.
	tmpFile, err = os.Create(tmpFile.Name())

	const sampleData2 = "example-2"
	// wait for some buffer time to see if the watcher can pick the file back up
	time.Sleep(3 * time.Second)
	if _, err := tmpFile.WriteString(sampleData2 + "\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-obsChan:
		assert.Equal(t, line, sampleData2)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestFileObserver_DirectoryPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()

	// The observed path exists but is a directory, as with a hostPath mount
	// of a file that is missing on the host.
	dirPath := filepath.Join(t.TempDir(), "messages")
	assert.NoError(t, os.Mkdir(dirPath, 0o755))

	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeFile]([]resource.Part{resource.Part(dirPath)})
	assert.NoError(t, err)

	obsChan := obs.Subscribe()
	var errorLogs atomic.Int64
	logger := logr.New(&errorCountingSink{count: &errorLogs})
	initCtx := log.IntoContext(ctx, logger)
	done := make(chan struct{})
	go func() {
		defer close(done)
		obs.Init(initCtx)
	}()

	select {
	case line := <-obsChan:
		t.Fatalf("unexpected line from a directory path: %q", line)
	case <-time.After(500 * time.Millisecond):
	}
	assert.Zero(t, errorLogs.Load(), "observer logged errors while the path was a directory")

	// Replacing the directory with a regular file must be picked up.
	assert.NoError(t, os.Remove(dirPath))
	f, err := os.Create(dirPath)
	assert.NoError(t, err)
	defer f.Close()

	const sampleData = "example"
	time.Sleep(500 * time.Millisecond)
	_, err = f.WriteString(sampleData + "\n")
	assert.NoError(t, err)

	select {
	case line := <-obsChan:
		assert.Equal(t, sampleData, line)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	cancel()
	<-done
}

// errorCountingSink is a logr.LogSink that counts Error calls and drops
// everything else.
type errorCountingSink struct {
	count *atomic.Int64
}

func (s *errorCountingSink) Init(logr.RuntimeInfo)          {}
func (s *errorCountingSink) Enabled(int) bool               { return false }
func (s *errorCountingSink) Info(int, string, ...any)       {}
func (s *errorCountingSink) Error(error, string, ...any)    { s.count.Add(1) }
func (s *errorCountingSink) WithValues(...any) logr.LogSink { return s }
func (s *errorCountingSink) WithName(string) logr.LogSink   { return s }
