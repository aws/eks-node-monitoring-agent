package packet_capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// TcpdumpCapturer implements the Capturer interface using tcpdump.
type TcpdumpCapturer struct{}

// NewTcpdumpCapturer creates a new TcpdumpCapturer.
func NewTcpdumpCapturer() *TcpdumpCapturer {
	return &TcpdumpCapturer{}
}

// buildTcpdumpArgs constructs the tcpdump command arguments from the spec.
func buildTcpdumpArgs(outputPath string, spec *v1alpha1.PacketCapture) []string {
	chunkSize := spec.ChunkSizeMB
	if chunkSize <= 0 {
		chunkSize = DefaultRotationSizeMB
	}

	args := []string{
		"-w", outputPath,                       // write capture to file
		"-C", fmt.Sprintf("%d", chunkSize),     // rotate after chunkSize MB
		"-W", MaxRotatedFiles,                  // max rotated files before wrapping
		"-U",                                   // packet-buffered output (flush per packet)
		"-z", CompressCmd,                      // post-rotate compress command
	}

	if spec.Interface != "" {
		args = append(args, "-i", spec.Interface)
	}

	// Filter expression must be the last argument per tcpdump convention
	if spec.Filter != "" {
		args = append(args, spec.Filter)
	}

	return args
}

// startFileWatcher polls the capture directory for completed .gz files, uploads them,
// and monitors disk usage. It runs synchronously — the caller starts it as a goroutine.
// It returns when stopCh is closed and all remaining files in the directory are processed.
func (t *TcpdumpCapturer) startFileWatcher(ctx context.Context, dir string, uploadConfig *UploadConfig, stopCh <-chan struct{}, terminateCh chan<- struct{}) []error {
	logger := log.FromContext(ctx).WithName("packet-capture")
	ticker := time.NewTicker(FileCheckInterval)
	defer ticker.Stop()

	var uploadErrors []error

	for {
		select {
		case <-stopCh:
			// Stop channel closed — process any remaining files before returning
			remaining, err := findGzipFiles(dir)
			if err != nil {
				logger.Error(err, "failed to find remaining gzip files during shutdown")
				return uploadErrors
			}
			for _, f := range remaining {
				if err := uploadFile(ctx, f, uploadConfig); err != nil {
					uploadErrors = append(uploadErrors, err)
					continue
				}
				_ = deleteCorrespondingPcapFile(f)
				if rmErr := os.Remove(f); rmErr != nil {
					logger.Error(rmErr, "failed to delete uploaded file", "fileName", filepath.Base(f))
				}
			}
			return uploadErrors

		case <-ticker.C:
			files, err := findGzipFiles(dir)
			if err != nil {
				logger.Error(err, "failed to find gzip files")
				continue
			}

			// List all files in capture dir for debugging
			allEntries, _ := os.ReadDir(dir)
			var allNames []string
			for _, e := range allEntries {
				allNames = append(allNames, e.Name())
			}
			logger.V(1).Info("file watcher poll", "gzFiles", len(files), "allFiles", allNames)

			for _, f := range files {
				if err := uploadFile(ctx, f, uploadConfig); err != nil {
					// Retry once
					logger.Info("upload failed, retrying", "fileName", filepath.Base(f))
					if retryErr := uploadFile(ctx, f, uploadConfig); retryErr != nil {
						uploadErrors = append(uploadErrors, retryErr)
						logger.Error(retryErr, "upload retry failed, terminating capture", "fileName", filepath.Base(f))
						close(terminateCh)
						return uploadErrors
					}
				}
				// Upload succeeded (first attempt or retry) — clean up
				_ = deleteCorrespondingPcapFile(f)
				if rmErr := os.Remove(f); rmErr != nil {
					logger.Error(rmErr, "failed to delete uploaded file", "fileName", filepath.Base(f))
				}
			}

			// Check disk usage
			usage, err := checkDiskSpace(dir)
			if err != nil {
				logger.Error(err, "failed to check disk space")
				continue
			}
			if usage > DiskUsageThreshold {
				logger.Info("disk usage exceeds threshold, terminating capture", "usage", usage, "threshold", DiskUsageThreshold)
				close(terminateCh)
				return uploadErrors
			}
		}
	}
}

// CaptureAndUpload implements the Capturer interface. It orchestrates the full
// capture lifecycle: start tcpdump, watch for rotated files, upload them, and
// run a shutdown sequence after the duration elapses or termination is signalled.
func (t *TcpdumpCapturer) CaptureAndUpload(ctx context.Context, config Config) ([]error, error) {
	logger := log.FromContext(ctx).WithName("packet-capture")

	// Parse duration
	duration, err := time.ParseDuration(config.Spec.Duration)
	if err != nil {
		return nil, fmt.Errorf("failed to parse duration %q: %w", config.Spec.Duration, err)
	}

	capturePath := filepath.Join(config.OutputPath, "capture.pcap")
	args := buildTcpdumpArgs(capturePath, config.Spec)

	logger.Info("starting tcpdump", "duration", duration, "interface", config.Spec.Interface, "chunkSizeMB", config.Spec.ChunkSizeMB, "outputPath", config.OutputPath)

	cmd := exec.CommandContext(ctx, "tcpdump", args...)
	var stderrBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start tcpdump: %w", err)
	}

	stopCh := make(chan struct{})
	terminateCh := make(chan struct{})

	var watcherErrors []error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		watcherErrors = t.startFileWatcher(ctx, config.OutputPath, config.UploadConfig, stopCh, terminateCh)
	}()

	// Wait for duration, termination signal, or context cancellation
	select {
	case <-time.After(duration):
		logger.Info("capture duration elapsed, stopping tcpdump")
	case <-terminateCh:
		logger.Info("file watcher requested termination")
	case <-ctx.Done():
		logger.Info("context cancelled")
	}

	// Send SIGTERM to tcpdump
	if cmd.Process != nil {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			logger.Info("failed to send SIGTERM to tcpdump (process may have already exited)", "error", err)
		}
	}
	var tcpdumpErr error
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGTERM {
				logger.Info("tcpdump terminated with SIGTERM")
			} else {
				logger.Error(err, "tcpdump exited with error")
				tcpdumpErr = err
			}
		} else {
			logger.Error(err, "tcpdump Wait() failed")
			tcpdumpErr = err
		}
	}

	// Log tcpdump output summary (not raw stderr to avoid confusion)
	if stderrBuf.Len() > 0 {
		stderrStr := stderrBuf.String()
		// Check if tcpdump reported an error vs normal stats
		if strings.Contains(stderrStr, "packets captured") {
			logger.Info("tcpdump completed", "output", stderrStr)
		} else {
			logger.Error(fmt.Errorf("tcpdump error"), "tcpdump reported an error", "output", stderrStr)
		}
	}

	// Signal file watcher to stop and wait for it
	close(stopCh)
	wg.Wait()

	// Shutdown sequence: gzip remaining pcap files, upload, cleanup
	remainingPcap, err := findNonGzipFiles(config.OutputPath, "capture.pcap")
	if err != nil {
		logger.Error(err, "failed to find remaining pcap files")
	} else {
		logger.Info("shutdown: found remaining pcap files", "count", len(remainingPcap), "files", remainingPcap)
		for _, f := range remainingPcap {
			if err := gzipFile(f); err != nil {
				logger.Error(err, "failed to gzip remaining file", "fileName", filepath.Base(f))
			}
		}
	}

	// Upload any remaining .gz files
	remainingGz, err := findGzipFiles(config.OutputPath)
	if err != nil {
		logger.Error(err, "failed to find remaining gz files")
	} else {
		logger.Info("shutdown: found remaining gz files", "count", len(remainingGz), "files", remainingGz)
		for _, f := range remainingGz {
			if err := uploadFile(ctx, f, config.UploadConfig); err != nil {
				watcherErrors = append(watcherErrors, err)
				logger.Error(err, "failed to upload remaining file", "fileName", filepath.Base(f))
				continue
			}
			if rmErr := os.Remove(f); rmErr != nil {
				logger.Error(rmErr, "failed to delete uploaded file", "fileName", filepath.Base(f))
			}
		}
	}

	return watcherErrors, tcpdumpErr
}
