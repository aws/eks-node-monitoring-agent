package autocapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/logcollection"
)

// CategoriesFor returns Base plus the category for the failed condition.
func CategoriesFor(condition corev1.NodeConditionType) []v1alpha1.LogCategory {
	categories := []v1alpha1.LogCategory{v1alpha1.LogCategoryBase}
	switch condition {
	case conditions.AcceleratedHardwareReady:
		categories = append(categories, v1alpha1.LogCategoryDevice)
	case conditions.NetworkingReady:
		categories = append(categories, v1alpha1.LogCategoryNetworking)
	case conditions.KernelReady:
		categories = append(categories, v1alpha1.LogCategorySystem)
	case conditions.ContainerRuntimeReady:
		categories = append(categories, v1alpha1.LogCategoryRuntime)
	}
	return categories
}

const (
	// A capture often fires because the disk is filling, and writing more could
	// push the node into DiskPressure. The kubelet evicts pods at 10% free by
	// default, so staying above that keeps a capture from getting the agent evicted.
	minFreePercent = 12

	logCaptureReportLog = "log-capture-errors.log"
	skipReasonLowDisk   = "low disk"
)

// ErrLowDisk means too little free disk to start a capture.
var ErrLowDisk = errors.New("not enough free disk space to start a log capture")

// Step is one finished collector. Dir is removed once the handler returns.
type Step struct {
	// Name is "<category>/<type>": the same collector type can appear in
	// several categories.
	Name     string
	Dir      string
	Failed   bool
	Files    int
	Bytes    int64
	Duration time.Duration
}

// StepFunc receives each finished collector while its folder still exists.
type StepFunc func(ctx context.Context, step Step) error

// Skipped is a collector that never ran, and why.
type Skipped struct {
	Name   string
	Reason string
}

// Summary describes a capture. Steps includes collectors whose folder
// could not be created.
type Summary struct {
	Steps        int
	Failed       int
	HandleErrors int
	Skipped      []Skipped
	Files        int
	Bytes        int64
}

// collectDeps are the host-touching pieces, faked in tests.
type collectDeps struct {
	freeDisk   func(dir string) (free, total uint64, err error)
	collectors func(categories ...v1alpha1.LogCategory) []collect.Collector
}

// CollectSteps runs the categories' collectors one at a time in a fixed order.
// Each gets its own temp folder, removed after handle, so only one output is on disk.
func CollectSteps(ctx context.Context, runtimeContext *config.RuntimeContext, categories []v1alpha1.LogCategory, handle StepFunc) (Summary, error) {
	return collectSteps(ctx, runtimeContext, categories, handle, collectDeps{
		freeDisk:   freeDiskBytes,
		collectors: logcollection.GetCollectors,
	})
}

func collectSteps(ctx context.Context, runtimeContext *config.RuntimeContext, categories []v1alpha1.LogCategory, handle StepFunc, deps collectDeps) (Summary, error) {
	logger := log.FromContext(ctx)
	var summary Summary

	queue := orderedCollectors(deps.collectors, categories)
	for i, next := range queue {
		// Stop instead of finishing a run no one will use.
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		if diskIsLow(logger, deps) {
			if i == 0 {
				return Summary{}, ErrLowDisk
			}
			// A partial capture still helps, so this is not a failure.
			for _, rest := range queue[i:] {
				summary.Skipped = append(summary.Skipped, Skipped{Name: rest.name, Reason: skipReasonLowDisk})
			}
			return summary, nil
		}

		summary.Steps++
		runStep(ctx, logger, runtimeContext, deps, next, handle, &summary)
	}
	return summary, nil
}

// diskIsLow counts a failed check as fine, so it cannot block an urgent capture.
func diskIsLow(logger logr.Logger, deps collectDeps) bool {
	free, total, err := deps.freeDisk(os.TempDir())
	if err != nil {
		logger.Error(err, "failed to check free disk space, continuing")
		return false
	}
	return free < total*minFreePercent/100
}

// runStep is its own function so the deferred RemoveAll runs even on panic.
func runStep(ctx context.Context, logger logr.Logger, runtimeContext *config.RuntimeContext, deps collectDeps, next namedCollector, handle StepFunc, summary *Summary) {
	started := time.Now()

	dir, err := os.MkdirTemp("", "eks-log-collector-*")
	if err != nil {
		logger.Error(err, "failed to create temp directory", "step", next.name)
		summary.Failed++
		return
	}
	defer os.RemoveAll(dir)

	step := Step{Name: next.name, Dir: dir}
	// One failed collector must not stop the loop: record it and keep going.
	if err := collectInto(ctx, runtimeContext, next.collector, dir); err != nil {
		logger.Error(err, "failed collection task", "step", next.name)
		report := fmt.Sprintf("--- Errors in collector %q ---\n%s\n", next.name, err)
		if err := os.WriteFile(filepath.Join(dir, logCaptureReportLog), []byte(report), 0644); err != nil {
			logger.Error(err, "failed to write capture errors", "step", next.name)
		}
		step.Failed = true
		summary.Failed++
	}

	step.Files, step.Bytes, err = dirSize(dir)
	if err != nil {
		logger.Error(err, "failed to measure collected logs", "step", next.name)
	}
	step.Duration = time.Since(started)
	summary.Files += step.Files
	summary.Bytes += step.Bytes

	// A handler failure loses only this step, so keep collecting.
	if err := handle(ctx, step); err != nil {
		logger.Error(err, "failed to handle collected step", "step", next.name)
		summary.HandleErrors++
	}
}

// collectInto runs one collector with its output written to dir.
func collectInto(ctx context.Context, runtimeContext *config.RuntimeContext, collector collect.Collector, dir string) error {
	// Fresh slice: appending to Tags() could write into the shared backing array.
	tags := append([]string{}, runtimeContext.Tags()...)
	tags = append(tags, runtimeContext.OSDistro(), runtimeContext.AcceleratedHardware())
	acc, err := collect.NewAccessor(ctx, collect.Config{
		Root:        config.HostRoot(),
		Destination: dir,
		Tags:        tags,
	})
	if err != nil {
		return fmt.Errorf("failed to create accessor: %w", err)
	}
	return collector.Collect(acc)
}

type namedCollector struct {
	name      string
	collector collect.Collector
}

// orderedCollectors calls get per category because its order across several
// categories is random.
func orderedCollectors(get func(...v1alpha1.LogCategory) []collect.Collector, categories []v1alpha1.LogCategory) []namedCollector {
	var queue []namedCollector
	for _, category := range categories {
		for _, collector := range get(category) {
			queue = append(queue, namedCollector{
				name:      fmt.Sprintf("%s/%s", category, reflect.TypeOf(collector).Elem().Name()),
				collector: collector,
			})
		}
	}
	return queue
}

// freeDiskBytes returns the space unprivileged writers can use under dir, and
// the filesystem's total size, so the limit can be a share of the disk.
func freeDiskBytes(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, fmt.Errorf("failed to stat filesystem %q: %w", dir, err)
	}
	// Field types differ between linux and darwin, so convert explicitly.
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}

// dirSize counts the regular files under dir and their total size.
func dirSize(dir string) (files int, size int64, err error) {
	err = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files++
			size += info.Size()
		}
		return nil
	})
	return files, size, err
}
