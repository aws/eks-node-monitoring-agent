package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/osext"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
	log "sigs.k8s.io/controller-runtime/pkg/log"
)

var _ monitor.Monitor = (*KernelMonitor)(nil)

type KernelMonitor struct {
	manager monitor.Manager
	logger  logr.Logger
}

func (m *KernelMonitor) Name() string {
	return "kernel"
}

func (m *KernelMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *KernelMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	m.logger = log.FromContext(ctx)

	dmesg, err := mgr.Subscribe(resource.ResourceTypeDmesg, []resource.Part{})
	if err != nil {
		return err
	}

	kubelet_log, err := mgr.Subscribe(resource.ResourceTypeJournal, []resource.Part{"kubelet"})
	if err != nil {
		return err
	}

	cron_log, err := mgr.Subscribe(resource.ResourceTypeFile, []resource.Part{resource.Part(config.ToHostPath("/var/log/cron.log"))})
	if err != nil {
		return err
	}

	for _, handler := range []interface{ Start(context.Context) error }{
		util.NewChannelHandler(m.handleDmesg, dmesg),
		util.NewChannelHandler(m.handleKubelet, kubelet_log),
		util.NewChannelHandler(makeCron(m).handle, cron_log),
		util.NewChannelHandler(func(time.Time) error { return m.handlePids() }, util.TimeTickWithJitter(5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleZombies() }, util.TimeTickWithJitter(5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleOpenedFiles() }, util.TimeTickWithJitter(5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleEnvironment() }, util.TimeTickWithJitter(5*time.Minute)),
	} {
		go handler.Start(ctx)
	}

	return nil
}

// ~~~~ dmesg ~~~~

var (
	kernelBugRegexp  = regexp.MustCompile(`\[.*?] BUG: (.*)`)
	softLockupRegexp = regexp.MustCompile(
		`watchdog: BUG: soft lockup - .* stuck for (.*)! \[(.*?).*\]`,
	)
	appCrash = regexp.MustCompile(strings.Join([]string{
		// each top level group is expected to have one sub group capture for the
		// process name
		`traps:\s*(.*?)\[`,
		`\s(.*?)\[\d+]: segfault at.*`,
	}, "|"))
)
var (
	appBlocked        = regexp.MustCompile(`task (.*?):\d+ blocked for more than`)
	conntrackExceeded = regexp.MustCompile(`(ip|nf)_conntrack: table full, dropping packet`)
)

func (k *KernelMonitor) handleDmesg(line string) error {
	if matches := softLockupRegexp.FindStringSubmatch(line); matches != nil {
		duration := matches[1]
		return k.manager.Notify(context.Background(),
			reasons.SoftLockup.
				Builder().
				Message(fmt.Sprintf("CPU stuck for %s", duration)).
				Build(),
		)
	} else if kernelBugRegexp.MatchString(line) {
		return k.manager.Notify(context.Background(),
			reasons.KernelBug.
				Builder().
				Message("A kernel bug was detected and reported by the Linux kernel").
				Build(),
		)
	} else if matches := appCrash.FindStringSubmatch(line); matches != nil {
		processName := matches[1]
		return k.manager.Notify(context.Background(),
			reasons.AppCrash.
				Builder().
				Message(fmt.Sprintf("Process %q on the node has crashed", processName)).
				Build(),
		)
	} else if matches := appBlocked.FindStringSubmatch(line); matches != nil {
		processName := matches[1]
		return k.manager.Notify(context.Background(),
			reasons.AppBlocked.
				Builder().
				Message(fmt.Sprintf("Process %q has been blocked from scheduling for a long period of time", processName)).
				Build(),
		)
	} else if conntrackExceeded.MatchString(line) {
		return k.manager.Notify(context.Background(),
			reasons.ConntrackExceededKernel.
				Builder().
				Message(fmt.Sprintf("Connection tracking exceeded the maximum for the kernel")).
				Build(),
		)
	}

	return nil
}

// ~~~~ kubelet logs ~~~~

var (
	forkFailedOutOfPidRegexp = regexp.MustCompile(`.*fork/exec.*resource temporarily unavailable`)
	goRuntimeOutOfPidRegexp  = regexp.MustCompile("failed to create new OS thread.*errno=11")
)

func (k *KernelMonitor) handleKubelet(line string) error {
	if forkFailedOutOfPidRegexp.MatchString(line) || goRuntimeOutOfPidRegexp.MatchString(line) {
		return k.manager.Notify(context.TODO(),
			reasons.ForkFailedOutOfPIDs.
				Builder().
				Message("A fork or exec call has failed due to the system being out of process IDs or memory").
				Build(),
		)
	}

	return nil
}

// ~~~~ opened files ~~~~

func (k *KernelMonitor) handleOpenedFiles() error {
	var allocated, total int64
	if _, err := osext.ParseSysctl("fs.file-nr", func(b []byte) (_ any, err error) {
		// utilizes the parser func to assign variables via the closure.
		filenrFields := strings.Fields(string(b))
		if allocated, err = strconv.ParseInt(filenrFields[0], 10, 64); err != nil {
			return nil, fmt.Errorf("error parsing allocated file handles: %w", err)
		}
		if total, err = strconv.ParseInt(filenrFields[2], 10, 64); err != nil {
			return nil, fmt.Errorf("error parsing total file handles: %w", err)
		}
		return nil, nil
	}); err != nil {
		return err
	}
	return k.checkOpenedFiles(float64(allocated), float64(total))
}

func (k *KernelMonitor) checkOpenedFiles(allocated, total float64) error {
	percentageUsed := float64(allocated) / float64(total)
	if percentageUsed < 0.7 {
		return nil
	}
	return k.manager.Notify(context.Background(),
		reasons.ApproachingMaxOpenFiles.
			Builder().
			Message(fmt.Sprintf("Approaching Exhaustion of max open file descriptors. %0.0f of %0.0f total, %0.1f%%", allocated, total, percentageUsed*100)).
			Build(),
	)
}

// ~~~~ kernel pids ~~~~

func (k *KernelMonitor) handlePids() error {
	pidMax, err := osext.ParseSysctl("kernel.pid_max", func(b []byte) (int, error) { return strconv.Atoi(string(b)) })
	if err != nil {
		return err
	}
	threadMax, err := osext.ParseSysctl("kernel.threads-max", func(b []byte) (int, error) { return strconv.Atoi(string(b)) })
	if err != nil {
		return err
	}
	if *pidMax < *threadMax {
		*pidMax = *threadMax
	}
	procs, err := filepath.Glob(config.ToHostPath("/proc/[0-9]*"))
	if err != nil {
		return err
	}
	pidCur := len(procs)
	return k.checkPids(pidCur, *pidMax)
}

func (k *KernelMonitor) checkPids(pidCur, pidMax int) error {
	percentageUsed := float64(pidCur) / float64(pidMax)
	if percentageUsed < 0.7 {
		return nil
	}
	return k.manager.Notify(context.Background(),
		reasons.ApproachingKernelPidMax.
			Builder().
			Message(fmt.Sprintf("Approaching max number of PIDs. %d of %d total, %0.1f%%", pidCur, pidMax, percentageUsed*100)).
			Build(),
	)
}

// ~~~~ zombies ~~~~

func (k *KernelMonitor) handleZombies() error {
	// Parse ps output and check lines for '<defunct>'
	out, err := osext.NewExec(config.HostRoot()).Command("ps", "aux").Output()
	if err != nil {
		return err
	}
	zombieCount := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "<defunct>") {
			zombieCount += 1
		}
	}
	return k.checkZombies(zombieCount)
}

func (k *KernelMonitor) checkZombies(zombieCount int) error {
	if zombieCount < 20 {
		return nil
	}
	return k.manager.Notify(context.Background(),
		reasons.ExcessiveZombieProcesses.
			Builder().
			Message(fmt.Sprintf("Detected %d zombie processes still running", zombieCount)).
			// this should run on a 5 minute cadence, so seeing this 5 times is
			// already 25 minutes of prolonged exposure.
			MinOccurrences(5).
			Build(),
	)
}

// ~~~~ cron ~~~~

// finding repeated cron jobs requires tracking some state, so this handler is
// implemented on top of a wrapper class.
type cron struct {
	*KernelMonitor
	cache map[string]time.Time
}

func makeCron(k *KernelMonitor) *cron {
	return &cron{
		KernelMonitor: k,
		cache:         make(map[string]time.Time),
	}
}

func (c *cron) handle(line string) error {
	if !strings.Contains(line, " CMD ") {
		return nil
	}
	segments := strings.Fields(line)
	if len(segments) < 7 {
		return fmt.Errorf("expected at least 7 fields in cron log entry")
	}
	constructedTimestamp := fmt.Sprintf("%s %s %s", segments[0], segments[1], segments[2])
	timestamp, err := time.Parse("Jan 2 15:04:05", constructedTimestamp)
	if err != nil {
		return err
	}
	command := segments[5]

	const minNumMiniutes = 5
	if lastSeen, ok := c.cache[command]; ok &&
		timestamp.Before(lastSeen.Add(minNumMiniutes*time.Minute)) {
		err = c.manager.Notify(context.TODO(),
			reasons.RapidCron.
				Builder().
				Message(fmt.Sprintf("A cron job is running faster than every %d minutes, which can impact node performance", minNumMiniutes)).
				Build(),
		)
	}
	c.cache[command] = timestamp

	return err
}

// ~~~~ environment ~~~~

func (k *KernelMonitor) handleEnvironment() error {
	envPaths, err := filepath.Glob(config.ToHostPath("/proc/[0-9]*/environ"))
	if err != nil {
		return err
	}
	for _, envPath := range envPaths {
		envBytes, err := os.ReadFile(envPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				// SAFETY: some processes may be ephemeral and will not appear
				// during iteration due to TOCTTOU. we can safely skip these.
				continue
			}
			k.logger.Error(err, "failed to process environ", "path", envPath)
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(envPath)))
		if err != nil {
			k.logger.Error(err, "could not parse a proc PID", "path", envPath)
			continue
		}
		if err := k.checkEnvironment(envBytes, pid); err != nil {
			return err
		}
	}
	return nil
}

func (k *KernelMonitor) checkEnvironment(envBytes []byte, pid int) error {
	// split by null character separator and discard empty entries.
	// see: https://www.man7.org/linux/man-pages/man7/environ.7.html
	var envs []string
	for _, env := range strings.Split(string(envBytes), "\x00") {
		if len(env) > 0 {
			envs = append(envs, env)
		}
	}
	if envCount := len(envs); envCount > 1000 {
		return k.manager.Notify(context.TODO(),
			reasons.LargeEnvironment.
				Builder().
				Message(fmt.Sprintf("PID %d has a higher than normal environment variable count at %d", pid, envCount)).
				Build(),
		)
	}
	return nil
}
