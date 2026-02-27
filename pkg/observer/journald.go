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

type journalObserver struct {
	BaseObserver
	journalBasePath string
	serviceName     string
}

func (o *journalObserver) Identifier() string {
	return "journal:" + o.serviceName
}

func (o *journalObserver) Init(ctx context.Context) error {
	journalPath := filepath.Join(config.HostRoot(), o.journalBasePath)

	journal, err := sdjournal.NewJournalFromDir(journalPath)
	if err != nil {
		return fmt.Errorf("failed to create journal client from path %q: %w", journalPath, err)
	}
	startTime := time.Now()
	if err = journal.SeekRealtimeUsec(util.TimeToJournalTimestamp(startTime)); err != nil {
		return fmt.Errorf("failed to seek journal at %v: %w", startTime, err)
	}
	match := sdjournal.Match{
		Field: sdjournal.SD_JOURNAL_FIELD_SYSLOG_IDENTIFIER,
		Value: o.serviceName,
	}
	if err := journal.AddMatch(match.String()); err != nil {
		return fmt.Errorf("failed to add journal filter for service %q due to: %s", o.serviceName, err)
	}
	return o.watchLoop(ctx, journal)
}

func (o *journalObserver) watchLoop(ctx context.Context, journal *sdjournal.Journal) error {
	logger := log.FromContext(ctx)
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		n, err := journal.Next()
		if err != nil {
			logger.Error(err, "failed to get next journal entry")
			return
		}
		if n == 0 {
			// Sleep until something happens on the journal
			journal.Wait(sdjournal.IndefiniteWait)
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
