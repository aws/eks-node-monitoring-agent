package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/moby/sys/mountinfo"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/storage/ebs"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/osext"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ monitor.Monitor = (*StorageMonitor)(nil)

const (
	IODelayCacheDefaultExpirationTime = 15 * time.Minute
)

type processIODetails struct {
	id    string
	name  string
	delay int
}

type StorageMonitor struct {
	manager    monitor.Manager
	log        logr.Logger
	delayCache cache.Store
}

func buildIODelayCacheKey(id string, name string) string {
	return fmt.Sprintf("%s-%s", id, name)
}

func delayCacheFunc(obj interface{}) (string, error) {
	return buildIODelayCacheKey(obj.(processIODetails).id, obj.(processIODetails).name), nil
}

func NewStorageMonitor() *StorageMonitor {
	return &StorageMonitor{
		delayCache: cache.NewTTLStore(delayCacheFunc, IODelayCacheDefaultExpirationTime),
	}
}

func (m *StorageMonitor) Name() string {
	return "storage"
}

func (m *StorageMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *StorageMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	m.log = log.FromContext(ctx)

	var_log_messages, err := mgr.Subscribe(resource.ResourceTypeFile, []resource.Part{resource.Part(config.SystemMessagesPath)})
	if err != nil {
		return err
	}

	kubelet_logs, err := mgr.Subscribe(resource.ResourceTypeJournal, []resource.Part{"kubelet"})
	if err != nil {
		return err
	}

	for _, handler := range []interface{ Start(context.Context) error }{
		util.NewChannelHandler(m.handleVarLogMessages, var_log_messages),
		util.NewChannelHandler(m.handleKubeletLogs, kubelet_logs),
		util.NewChannelHandler(func(time.Time) error { return m.handleXFS() }, util.TimeTickWithJitterContext(ctx, 10*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleIODelays() }, util.TimeTickWithJitterContext(ctx, 10*time.Minute)),
	} {
		go handler.Start(ctx)
	}

	// Periodic cache cleanup to trigger lazy TTL expiration
	// This prevents unbounded growth from dead processes that are never accessed again
	go func() {
		for range util.TimeTickWithJitterContext(ctx, IODelayCacheDefaultExpirationTime) {
			m.delayCache.List()
		}
	}()

	// EBS NVMe throttling monitoring (runs on all nodes with NVMe devices)
	ebsSystem := ebs.NewEBSSystem()
	go func() {
		for range util.TimeTickWithJitterContext(ctx, 10*time.Minute) {
			conditions, err := ebsSystem.NVMeThrottles(ctx)
			if err != nil {
				m.log.Error(err, "failed to check EBS NVMe throttles")
				continue
			}
			for _, condition := range conditions {
				if err := m.manager.Notify(ctx, condition); err != nil {
					m.log.Error(err, "failed to notify EBS condition")
				}
			}
		}
	}()

	return nil
}

var etcHostsIsDirRegexp = regexp.MustCompile(`error mounting.*etc-hosts.*to rootfs.*/etc/hosts`)

func (m *StorageMonitor) handleVarLogMessages(line string) error {
	if etcHostsIsDirRegexp.MatchString(line) {
		return m.manager.Notify(context.Background(),
			reasons.EtcHostsMountFailed.
				Builder().
				Message("Mounting of the kubelet generated /etc/hosts failed").
				Build(),
		)
	}
	return nil
}

var diskUsageSlow = regexp.MustCompile(`fs: disk usage and inodes count on following dirs took`)

func (m *StorageMonitor) handleKubeletLogs(line string) error {
	if diskUsageSlow.MatchString(line) {
		return m.manager.Notify(context.Background(),
			reasons.KubeletDiskUsageSlow.
				Builder().
				Message("Kubelet is reporting slow disk usage, which may indicate a misconfigured filesystem or insufficient disk I/O").
				Build(),
		)
	}
	return nil
}

// ~~~~ XFS ~~~~

var avgFreeExtent = regexp.MustCompile(`average free extent size ([-+]?[0-9]*\.?[0-9]+([eE][-+]?[0-9]+)?)`)

func (m *StorageMonitor) handleXFS() error {
	xfsInfo, err := mountinfo.GetMounts(func(info *mountinfo.Info) (skip, stop bool) {
		return info.FSType != "xfs", false
	})
	if err != nil {
		return err
	}
	uniqueXfsInfo := map[string]*mountinfo.Info{}
	for _, xfs := range xfsInfo {
		uniqueXfsInfo[xfs.Source] = xfs
	}
	exec := osext.NewExec(config.HostRoot())
	for xfs := range uniqueXfsInfo {
		xfsInfoBytes, err := exec.Command("xfs_db", "-r", "-c", "freesp -s", xfs).Output()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(xfsInfoBytes), "\n") {
			if matches := avgFreeExtent.FindStringSubmatch(line); len(matches) > 0 {
				avgSize, err := strconv.ParseFloat(matches[1], 64)
				if err != nil {
					// skip the filesystem if we could not properly parse the
					// average size from the string.
					continue
				}
				m.checkXFS(avgSize)
			}
		}
	}
	return nil
}

func (m *StorageMonitor) checkXFS(avgSize float64) error {
	// TODO: collect data to get an accurate value here, for now we're starting
	// with a lower threshold to avoid noise.
	if avgSize < 16 {
		return m.manager.Notify(context.Background(),
			reasons.XFSSmallAverageClusterSize.
				Builder().
				Message("The XFS Average Cluster size is small, which may prevent new files from being created due to free space fragmentation").
				Build(),
		)
	}
	return nil
}

// ~~~~ IO ~~~~

// see https://man7.org/linux/man-pages/man5/proc.5.html
const (
	colPid = iota
	colComm
	colState
	colPpid
	colPgrp
	colSession
	colTtyNr
	colTgpid
	colFlags
	colMinflt
	colCminflt
	colMajflt
	colCmajflt
	colUtime
	colStime
	colCutime
	colCstime
	colPriority
	colNice
	colNumThreads
	colItrealvalue
	colStarttime
	colVsize
	colRss
	colRssLim
	colSTartcode
	colEndcode
	colStartstack
	colKstkesp
	colKstkeip
	colSignal
	colBlocked
	colSigignore
	colSigcatch
	colWchan
	colNswap
	colCnswap
	colExitsignal
	colProcessor
	colRtpriority
	colPolicy
	colDelayacct_blkio_ticks
)

const (
	// delay is measured in clock ticks (centiseconds).
	centisecondsPerSecond = 100

	ioDelayThresholdSeconds = 10
)

func (m *StorageMonitor) handleIODelays() (merr error) {
	if procPaths, err := filepath.Glob(config.ToHostPath("/proc/[0-9]*/stat")); err != nil {
		merr = errors.Join(merr, err)
	} else {
		for _, entry := range procPaths {
			fBytes, err := os.ReadFile(entry)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					// SAFETY: some processes may be ephemeral and will not appear
					// during iteration. we can safely skip these.
					continue
				}
				merr = errors.Join(merr, err)
				continue
			}
			merr = errors.Join(merr, m.checkIODelays(fBytes))
		}
	}
	return merr
}

func (m *StorageMonitor) checkIODelays(procBytes []byte) error {
	segments := strings.Fields(string(procBytes))
	if len(segments) < colDelayacct_blkio_ticks /* last field index */ {
		return fmt.Errorf("number of field in proc file should be at least %d", colDelayacct_blkio_ticks+1)
	}
	currentDelay, err := strconv.Atoi(segments[colDelayacct_blkio_ticks])
	if err != nil {
		return fmt.Errorf("failed to parse block I/O delay: %s", err)
	}
	previousDelay, exists, _ := m.delayCache.GetByKey(buildIODelayCacheKey(segments[colPid], segments[colComm]))
	m.delayCache.Add(processIODetails{
		id:    segments[colPid],
		name:  segments[colComm],
		delay: currentDelay,
	})
	if !exists {
		// We're seeing this process for the first time, so we can't calculate
		// a delta and return.
		return nil
	}
	delay := currentDelay - previousDelay.(processIODetails).delay
	delayInSeconds := float64(delay) / float64(centisecondsPerSecond)
	// ignore anything below threshold
	if delayInSeconds < ioDelayThresholdSeconds {
		return nil
	}
	return m.manager.Notify(context.TODO(),
		reasons.IODelays.
			Builder().
			Message(fmt.Sprintf("Process %s (PID %s) incurred %0.1f seconds of I/O delay", segments[colComm], segments[colPid], delayInSeconds)).
			Build(),
	)
}
