package observer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
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
