//go:build linux

package observer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/util"
	"github.com/coreos/go-systemd/v22/sdjournal"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func init() {
	RegisterObserverConstructor(resource.ResourceTypeJournal, func(rp []resource.Part) (Observer, error) {
		if l := len(rp); l != 1 {
			return nil, fmt.Errorf("part count must be 1, but was %d", l)
		}
		return &journalObserver{
			serviceName:     string(rp[0]),
			journalBasePath: resolveJournalPath(),
		}, nil
	})
}

var journalPaths = []string{"/var/log/journal", "/run/log/journal"}

const (
	// journalRefreshInterval bounds a single sd_journal handle's lifetime.
	// sd_journal keeps an FD and mmap window per file in the journal dir, times
	// two observers, so a long-lived handle accumulates FDs as journald rotates.
	// We only tail forward, so periodically reopening bounds the open set to the
	// files currently on disk.
	journalRefreshInterval = 5 * time.Minute
	// journalWaitTimeout bounds journal.Wait so the loop wakes to check the
	// refresh timer even when the journal is idle.
	journalWaitTimeout = 5 * time.Second
)

type journalObserver struct {
	BaseObserver
	journalBasePath string
	serviceName     string
}

func (o *journalObserver) Identifier() string {
	return "journal:" + o.serviceName
}

func (o *journalObserver) Init(ctx context.Context) error {
	journal, err := o.openJournal()
	if err != nil {
		return err
	}
	return o.watchLoop(ctx, journal)
}

// openJournal opens a journal handle for this observer's service, positioned at
// "now" so that only new entries are tailed. The caller owns the returned
// handle and must Close it.
func (o *journalObserver) openJournal() (*sdjournal.Journal, error) {
	journalPath := filepath.Join(config.HostRoot(), o.journalBasePath)

	journal, err := sdjournal.NewJournalFromDir(journalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create journal client from path %q: %w", journalPath, err)
	}
	startTime := time.Now()
	if err := journal.SeekRealtimeUsec(util.TimeToJournalTimestamp(startTime)); err != nil {
		journal.Close()
		return nil, fmt.Errorf("failed to seek journal at %v: %w", startTime, err)
	}
	match := sdjournal.Match{
		Field: sdjournal.SD_JOURNAL_FIELD_SYSLOG_IDENTIFIER,
		Value: o.serviceName,
	}
	if err := journal.AddMatch(match.String()); err != nil {
		journal.Close()
		return nil, fmt.Errorf("failed to add journal filter for service %q due to: %s", o.serviceName, err)
	}
	return journal, nil
}

func (o *journalObserver) watchLoop(ctx context.Context, journal *sdjournal.Journal) error {
	logger := log.FromContext(ctx)
	// Close whichever handle is current when the loop exits. A closure is used
	// so it observes reassignments of journal made during refresh below.
	defer func() { journal.Close() }()

	lastRefresh := time.Now()
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		// Refresh the handle to release FDs/mmaps for rotated or vacuumed files.
		// Safe on this single goroutine (sd_journal isn't thread-safe); reseeking
		// to "now" loses at most the window between close and reopen.
		if time.Since(lastRefresh) >= journalRefreshInterval {
			if refreshed, err := o.openJournal(); err != nil {
				logger.Error(err, "failed to refresh journal handle; keeping existing handle")
			} else {
				journal.Close()
				journal = refreshed
			}
			// Advance the timer even on failure, so a failing refresh retries
			// once per interval rather than on every loop iteration.
			lastRefresh = time.Now()
		}

		n, err := journal.Next()
		if err != nil {
			logger.Error(err, "failed to get next journal entry")
			return
		}
		if n == 0 {
			// Wait (bounded) so the loop wakes to re-check the refresh timer
			// even when no new entries arrive.
			journal.Wait(journalWaitTimeout)
			return
		}
		entry, err := journal.GetEntry()
		if err != nil {
			logger.Error(err, "failed to get journal entry")
			return
		}
		message := strings.TrimSpace(entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE])
		o.Broadcast(o.Identifier(), message)
	}, 0)
	return nil
}

func resolveJournalPath() string {
	for _, path := range journalPaths {
		journalPath := filepath.Join(config.HostRoot(), path)
		if _, err := os.Stat(journalPath); err == nil {
			return path
		}
	}
	return ""
}
