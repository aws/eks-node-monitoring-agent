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

func cpuThrottles(acc *Accessor) error {
	var (
		merr          error
		buf           bytes.Buffer
		throttledPids []string
	)

	if err := filepath.Walk(filepath.Join(acc.cfg.Root, "/sys/fs/cgroup/"), func(path string, info fs.FileInfo, err error) error {
		// search the cpu.stat files to get throttle counts
		if filepath.Base(path) != "cpu.stat" {
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			merr = errors.Join(merr, err)
			// dont fail the entire task
			return nil
		}
		// iterate through lines of the cpu.stat file
		// formatted like:
		// ---
		// nr_periods 0
		// nr_throttled 0
		// throttled_time 0
		for _, line := range strings.Split(string(bytes), "\n") {
			if len(line) == 0 {
				continue
			}
			parts := strings.Split(line, " ")
			if len(parts) != 2 {
				merr = errors.Join(merr, fmt.Errorf("incorrect cpu.stat entry %q", line))
				continue
			}
			// watch for the nr_throttled field to detect CPU throttles
			if parts[0] == "nr_throttled" {
				count, err := strconv.Atoi(parts[1])
				if err != nil {
					merr = errors.Join(merr, err)
					continue
				}
				if count > 0 {
					// modify the path to read all processes from the cgroup
					// that we know got throttled. the file is a list of PIDs
					pidListBytes, err := os.ReadFile(strings.ReplaceAll(path, "cpu.stat", "cgroup.procs"))
					if err != nil {
						merr = errors.Join(merr, err)
						continue
					}
					for _, pid := range strings.Split(string(pidListBytes), "\n") {
						throttledPids = append(throttledPids, pid)
					}
				}
			}
		}
		// walk should never exit badly
		return nil
	}); err != nil {
		return errors.Join(merr, err)
	}

	psOut, err := acc.Command("ps", "ax").CombinedOutput()
	if err != nil {
		return errors.Join(merr, err)
	}

	for _, psLine := range strings.Split(string(psOut), "\n") {
		for _, pid := range throttledPids {
			// save the entire ps entry if the pid is from the throttled list
			if strings.HasPrefix(psLine, pid) {
				buf.WriteString(psLine + "\n")
				break
			}
		}
	}

	if buf.Len() == 0 {
		return errors.Join(merr, acc.WriteOutput("system/cpu_throttling.txt", []byte("No CPU Throttling Found")))
	} else {
		return errors.Join(merr, acc.WriteOutput("system/cpu_throttling.txt", buf.Bytes()))
	}
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
		errors.Join(merr, err)
	}
	return errors.Join(merr, acc.WriteOutput("system/io_throttling.txt", buf.Bytes()))
}
