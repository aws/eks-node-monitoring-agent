package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type Throttles struct{}

func (p *Throttles) Collect(acc *Accessor) error {
	return errors.Join(
		ioThrottles(acc),
		cpuThrottles(acc),
	)
}

// throttledCgroup is one cgroup whose cpu.stat reported nr_throttled > 0: its
// path (so the reader knows which cgroup throttled), the full cpu.stat
// contents (how often and for how long), and the PIDs it held.
type throttledCgroup struct {
	path string
	stat string
	pids []string
}

func cpuThrottles(acc *Accessor) error {
	cgroups, merr := gatherThrottledCgroups(acc)
	if len(cgroups) == 0 {
		return errors.Join(merr, acc.WriteOutput("system/cpu_throttling.txt", []byte("No CPU Throttling Found")))
	}

	psOut, err := acc.CombinedOutput("ps", "ax")
	if err != nil {
		return errors.Join(merr, err)
	}
	return errors.Join(merr, acc.WriteOutput("system/cpu_throttling.txt", renderThrottles(cgroups, string(psOut))))
}

// gatherThrottledCgroups walks the cgroup hierarchy and returns every cgroup
// whose cpu.stat has nr_throttled > 0 and which holds at least one PID.
// Collection errors are joined and returned alongside the partial result so
// one unreadable cgroup does not fail the capture.
func gatherThrottledCgroups(acc *Accessor) ([]throttledCgroup, error) {
	var (
		merr    error
		cgroups []throttledCgroup
	)
	if err := filepath.Walk(filepath.Join(acc.cfg.Root, "/sys/fs/cgroup/"), func(path string, info fs.FileInfo, err error) error {
		// search the cpu.stat files to get throttle counts
		if filepath.Base(path) != "cpu.stat" {
			return nil
		}
		statBytes, err := os.ReadFile(path)
		if err != nil {
			merr = errors.Join(merr, err)
			// dont fail the entire task
			return nil
		}
		// cpu.stat is formatted like:
		// ---
		// nr_periods 0
		// nr_throttled 0
		// throttled_time 0
		for _, line := range strings.Split(string(statBytes), "\n") {
			if len(line) == 0 {
				continue
			}
			parts := strings.Split(line, " ")
			if len(parts) != 2 {
				merr = errors.Join(merr, fmt.Errorf("incorrect cpu.stat entry %q", line))
				continue
			}
			if parts[0] != "nr_throttled" {
				continue
			}
			count, err := strconv.Atoi(parts[1])
			if err != nil {
				merr = errors.Join(merr, err)
				continue
			}
			if count == 0 {
				continue
			}
			// the sibling cgroup.procs file lists the cgroup's PIDs, one per
			// line, ending with a newline: empty lines must be skipped or the
			// empty string would later match every ps entry.
			pidListBytes, err := os.ReadFile(filepath.Join(filepath.Dir(path), "cgroup.procs"))
			if err != nil {
				merr = errors.Join(merr, err)
				continue
			}
			var pids []string
			for _, pid := range strings.Split(string(pidListBytes), "\n") {
				// "0" is how the kernel renders a PID that is not mappable
				// into the reader's PID namespace — not a real process.
				if pid = strings.TrimSpace(pid); pid != "" && pid != "0" {
					pids = append(pids, pid)
				}
			}
			if len(pids) == 0 {
				continue
			}
			cgroups = append(cgroups, throttledCgroup{
				// report the host path, not the agent's Root-prefixed view
				path: strings.TrimPrefix(filepath.Dir(path), acc.cfg.Root),
				stat: string(statBytes),
				pids: pids,
			})
		}
		// walk should never exit badly
		return nil
	}); err != nil {
		merr = errors.Join(merr, err)
	}
	return cgroups, merr
}

// renderThrottles writes one section per throttled cgroup: its path, its full
// cpu.stat, and the ps entry of each of its PIDs. PIDs are matched against the
// first field of each ps line exactly — ps right-aligns the PID column, so a
// prefix match against the raw line would both miss real PIDs and let a short
// PID match a longer one.
func renderThrottles(cgroups []throttledCgroup, psOutput string) []byte {
	psByPID := make(map[string]string)
	for _, line := range strings.Split(psOutput, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			psByPID[fields[0]] = line
		}
	}

	var buf bytes.Buffer
	for _, cgroup := range cgroups {
		buf.WriteString(cgroup.path + "\n")
		buf.WriteString(strings.TrimRight(cgroup.stat, "\n") + "\n")
		for _, pid := range cgroup.pids {
			if line, ok := psByPID[pid]; ok {
				buf.WriteString(line + "\n")
			}
			// a PID with no ps line exited between the cgroup read and ps;
			// the cgroup section above still records that throttling happened
		}
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

func ioThrottles(acc *Accessor) error {
	var (
		merr error
		buf  bytes.Buffer
	)
	buf.WriteString("PID Name Block IO Delay (centisconds)")
	procs, err := filepath.Glob(filepath.Join(acc.cfg.Root, "/proc/[0-9]*/stat"))
	if err != nil {
		return err
	}

	type ioEntry struct {
		id     string
		name   string
		metric int
	}
	var entries []ioEntry
	for _, proc := range procs {
		data, err := os.ReadFile(proc)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// SAFETY: some processes may exit after being listed and before
				// iteration. we can safely skip these.
				continue
			}
			merr = errors.Join(merr, err)
			continue
		}
		parts := strings.Split(string(data), " ")
		// column 42 (1-indexed) is Aggregated block I/O delays, measured in
		// centiseconds so we capture the non-zero block I/O delays.
		if len(parts) < 41+1 {
			merr = errors.Join(merr, fmt.Errorf("incorrect number of arguments in file"))
			continue
		}
		if parts[41] != "0" {
			metric, err := strconv.Atoi(parts[41])
			if err != nil {
				merr = errors.Join(merr, fmt.Errorf("incorrect format of value %q: %w", parts[41], err))
				continue
			}
			entries = append(entries, ioEntry{
				id:     parts[0],
				name:   parts[1],
				metric: metric,
			})
		}
	}
	slices.SortFunc(entries, func(a, b ioEntry) int {
		if a.metric == b.metric {
			return 0
		} else if a.metric < b.metric {
			return 1
		} else {
			return -1
		}
	})
	for _, entry := range entries {
		_, err := buf.WriteString(fmt.Sprintf("\n%s %s %d", entry.id, entry.name, entry.metric))
		merr = errors.Join(merr, err)
	}
	return errors.Join(merr, acc.WriteOutput("system/io_throttling.txt", buf.Bytes()))
}
