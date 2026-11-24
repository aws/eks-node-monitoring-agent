package collect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultMemorySampleInterval is the time between the two samples of
// /proc/vmstat.
const DefaultMemorySampleInterval = 10 * time.Second

// Memory collects /proc/meminfo once and /proc/vmstat twice, Interval apart,
// for inclusion in the diagnostic log bundle.
//
// /proc/meminfo holds current values, so one sample shows the state. Most of
// /proc/vmstat are counters since boot: one sample gives the totals, which look
// the same on a node that is reclaiming or swapping now and on one that did so
// hours ago. The second sample turns them into a rate over Interval.
//
// The sample times are written to system/vmstat_times.txt, so the rate can be
// computed without knowing the interval that was configured.
type Memory struct {
	// Interval is the time between the two /proc/vmstat samples. Defaults to
	// DefaultMemorySampleInterval.
	Interval time.Duration
}

var _ Collector = (*Memory)(nil)

func (m *Memory) Collect(acc *Accessor) error {
	interval := m.Interval
	if interval <= 0 {
		interval = DefaultMemorySampleInterval
	}

	// a missing file is skipped rather than failing the bundle, as in Pressure.
	copyIfPresent := func(path, dst string) error {
		err := acc.CopyFile(filepath.Join(acc.cfg.Root, path), dst)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	merr := copyIfPresent("/proc/meminfo", "system/meminfo.txt")

	if _, err := os.Stat(filepath.Join(acc.cfg.Root, "/proc/vmstat")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return merr
		}
		return errors.Join(merr, err)
	}

	first := time.Now().UTC()
	if err := copyIfPresent("/proc/vmstat", "system/vmstat_1.txt"); err != nil {
		// without a first sample a second one has nothing to be compared with.
		return errors.Join(merr, err)
	}
	times := fmt.Sprintf("vmstat_1.txt %s\n", first.Format(time.RFC3339Nano))

	select {
	case <-time.After(interval):
	case <-acc.ctx.Done():
		// keep the first sample; the capture is being stopped.
		return errors.Join(merr, acc.WriteOutput("system/vmstat_times.txt", []byte(times)), acc.ctx.Err())
	}

	second := time.Now().UTC()
	merr = errors.Join(merr, copyIfPresent("/proc/vmstat", "system/vmstat_2.txt"))
	times += fmt.Sprintf("vmstat_2.txt %s\n", second.Format(time.RFC3339Nano))
	return errors.Join(merr, acc.WriteOutput("system/vmstat_times.txt", []byte(times)))
}
