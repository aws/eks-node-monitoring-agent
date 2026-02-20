package observer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func init() {
	RegisterObserverConstructor(resource.ResourceTypeFile, func(rp []resource.Part) (Observer, error) {
		if l := len(rp); l != 1 {
			return nil, fmt.Errorf("part count must be 1, but was %d", l)
		}
		return &fileObserver{path: string(rp[0])}, nil
	})
}

type fileObserver struct {
	BaseObserver
	path string

	watcher    *fsnotify.Watcher
	fileHandle *os.File
	fileReader *bufio.Reader
}

func (o *fileObserver) Identifier() string {
	return "file:" + o.path
}

func (o *fileObserver) Init(ctx context.Context) error {
	return o.watchLoop(ctx)
}

func (o *fileObserver) watchLoop(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Wait for the reader to be successfully initialized
	wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (done bool, err error) {
		err = o.openReader()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.V(5).Info("failed initializing reader", "error", err)
		}
		return err == nil, nil
	})
	logger.Info("initialized reader")
	defer o.fileHandle.Close()

	wait.UntilWithContext(ctx, func(ctx context.Context) {
		workFunc := func() error {
			if err := o.reconcileWatcher(); err != nil {
				return err
			}
			select {
			case err := <-o.watcher.Errors:
				return fmt.Errorf("fsnotify watch error: %w", err)
			case ev := <-o.watcher.Events:
				if ev.Name == o.path {
					if ev.Op.Has(fsnotify.Create) {
						if err := o.openReader(); err != nil {
							return fmt.Errorf("failed to construct reader: %w", err)
						}
					}
				}
			default:
				err := o.readAll()
				if err != nil && err != io.EOF {
					return fmt.Errorf("failed to read file: %w", err)
				}
				if err == io.EOF {
					time.Sleep(100 * time.Millisecond)
					return nil
				}
			}
			return nil
		}
		if err := workFunc(); err != nil {
			logger.Error(err, "error in workloop")
		}
	}, 0)

	return nil
}

func (o *fileObserver) readAll() error {
	for {
		line, err := o.fileReader.ReadString('\n')
		if err != nil {
			return err
		}
		if len(line) > 0 {
			o.Broadcast(o.Identifier(), strings.TrimSpace(line))
		}
	}
}

func (o *fileObserver) reconcileWatcher() error {
	var err error
	if o.watcher == nil {
		o.watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return err
		}
	}
	watchDir := filepath.Dir(o.path)
	if !slices.Contains(o.watcher.WatchList(), watchDir) {
		if err := o.watcher.Add(watchDir); err != nil {
			return err
		}
	}
	return nil
}

func (o *fileObserver) openReader() error {
	var err error
	if o.fileHandle != nil {
		o.fileHandle.Close()
	}
	o.fileHandle, err = os.Open(o.path)
	if err != nil {
		return err
	}
	// Seek to end of file to ignore old data
	if _, err := o.fileHandle.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	o.fileReader = bufio.NewReader(o.fileHandle)
	return nil
}
