package system

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

// Top is a reimplementation of the `top` command output since that binary isn't available in BottleRocket and we don't
// want to add it.
func Top(w io.Writer, rootPath string) error {
	top := topCmd{rootPath: rootPath}
	return top.Run(w)
}

type topCmd struct {
	// the rootPath to use, which can depend on mounts if executing in a Pod
	rootPath string
}

func (t *topCmd) Run(w io.Writer) error {
	uptime, err := t.getUptime()
	if err != nil {
		return err
	}
	loadAvg, err := t.getLoadAverage()
	if err != nil {
		return err
	}
	// top - 16:49:49 up 16 days, 14:57,  1 user,  load average: 0.23, 0.05, 0.02
	fmt.Fprintf(w, "top - %s %s, load average: %.2f, %.2f, %.2f\n",
		time.Now().Format("15:04:05"),
		uptime,
		loadAvg.oneMin,
		loadAvg.fiveMin,
		loadAvg.tenMin,
	)
	processes, err := process.Processes()
	if err != nil {
		return err
	}

	t.writeTaskLine(w, processes)
	t.writeCPUInfo(w)
	t.writeMemoryInfo(w)

	sort.Slice(processes, func(i, j int) bool {
		lhsPct, _ := processes[i].CPUPercent()
		rhsPct, _ := processes[j].CPUPercent()
		return rhsPct > lhsPct
	})

	tw := tabwriter.NewWriter(w, 2, 2, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintf(tw, "PID\tUSER\tNI\tVIRT\tRES\t%%CPU\t%%MEM\tTIME+\tCOMMAND\n")
	// TODO: sort processes by PID
	for _, p := range processes {
		user, _ := p.Username()

		nice, _ := unix.Getpriority(unix.PRIO_PROCESS, int(p.Pid))
		// system nice value is [40,1] but displayed as [-20,19]
		nice = -1*nice + 20

		memory, _ := p.MemoryInfo()
		if memory == nil {
			memory = &process.MemoryInfoStat{}
		}

		cpuPct, _ := p.CPUPercent()
		memPct, _ := p.MemoryPercent()
		name, _ := p.Name()
		var usageSeconds float64
		if times, err := p.Times(); err == nil {
			usageSeconds = times.User + times.System + times.Idle + times.Nice + times.Iowait + times.Irq +
				times.Softirq + times.Steal + times.Guest + times.GuestNice
		}

		secs := math.Mod(usageSeconds, 60.0)
		mins := int(usageSeconds / 60)
		var ageString string
		if mins > 0 {
			ageString = fmt.Sprintf("%0d:%05.2f", mins, secs)
		} else {
			ageString = fmt.Sprintf("%.2f", secs)
		}

		fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%d\t%0.1f\t%0.1f\t%s\t%s\n", p.Pid, user, nice,
			memory.VMS/1024, memory.RSS/1024,
			cpuPct, memPct,
			ageString,
			name,
		)
	}
	return nil
}

func (t *topCmd) writeMemoryInfo(w io.Writer) error {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "KiB Mem : %d Total, %d free, %d used, %d buffers/cache\n",
		vm.Total/1024, vm.Free/1024, vm.Used/1024, (vm.Buffers+vm.Cached)/1024)
	fmt.Fprintf(w, "KiB Swap: %d total, %d free, %d used.\n",
		vm.SwapTotal, vm.SwapFree, vm.SwapTotal-vm.SwapFree)
	return nil
}

type procInfo struct {
	label  string
	total  int64
	values map[string]int64
}

func (i procInfo) subtract(rhs procInfo) procInfo {
	values := map[string]int64{}
	for k, v := range i.values {
		values[k] = v - rhs.values[k]
	}
	return procInfo{
		label:  i.label,
		total:  i.total - rhs.total,
		values: values,
	}
}

func (t *topCmd) getProcInfo() ([]procInfo, error) {
	f, err := os.Open(filepath.Join(t.rootPath, "/proc/stat"))
	if err != nil {
		return nil, fmt.Errorf("open /proc/stat: %w", err)
	}
	defer f.Close()

	var result []procInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		var pi procInfo
		fields := strings.Fields(line)
		fieldValues := map[string]int64{}
		for fieldName, fieldIdx := range map[string]int{
			"user":       1,
			"nice":       2,
			"system":     3,
			"idle":       4,
			"iowait":     5,
			"irq":        6,
			"softirq":    7,
			"steal":      8,
			"guest":      9,
			"guest_nice": 9,
		} {
			if fieldIdx >= len(fields) {
				continue
			}
			value, err := strconv.ParseInt(fields[fieldIdx], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parsing %s %q: %w", fieldName, fields[fieldIdx], err)
			}
			fieldValues[fieldName] = value
			pi.total += value
		}

		pi.values = fieldValues
		pi.label = initialCase(fields[0])
		if fields[0] == "cpu" {
			pi.label += "(s)"
		}
		result = append(result, pi)
	}
	return result, nil
}

func (t *topCmd) writeCPUInfo(w io.Writer) error {
	start, err := t.getProcInfo()
	if err != nil {
		return err
	}
	time.Sleep(1 * time.Second)
	end, err := t.getProcInfo()
	if err != nil {
		return err
	}

	var result []procInfo
	for i := 0; i < len(start); i++ {
		result = append(result, end[i].subtract(start[i]))
	}
	for _, cpu := range result {
		pct := func(label string) float64 {
			return float64(cpu.values[label]) / float64(cpu.total) * 100
		}
		fmt.Fprintf(w, "%%%s: %0.1f us, %0.1fsy, %0.1f ni, %0.1f id, %0.1f wa, %0.1f hi, %0.1f si, %0.1f st\n",
			cpu.label,
			pct("user"),
			pct("system"),
			pct("nice"),
			pct("idle"),
			pct("iowait"),
			pct("irq"),
			pct("softirq"),
			pct("steal"),
		)
	}

	return nil
}

func initialCase(s string) string {
	rs := []rune(s)
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

func (t *topCmd) writeTaskLine(w io.Writer, processes []*process.Process) {
	var totalTasks, idleTasks, runningTasks, stoppedTasks, sleepingTasks, zombieTasks int
	for _, p := range processes {
		status, err := p.Status()
		if err != nil {
			// TODO
		}
		totalTasks++
		if len(status) == 0 {
			continue
		}
		switch status[0] {
		case process.Idle:
			idleTasks++
		case process.Running:
			runningTasks++
		case process.Sleep:
			sleepingTasks++
		case process.Stop:
			stoppedTasks++
		case process.Zombie:
			zombieTasks++
		}
	}
	fmt.Fprintf(w, "Tasks: %d total, %d running, %d sleeping, %d stopped, %d zombie\n",
		totalTasks, runningTasks, sleepingTasks, stoppedTasks, zombieTasks)
}

type loadAverage struct {
	oneMin           float64
	fiveMin          float64
	tenMin           float64
	runningProcesses int
	totalProcesses   int
	lastPidUsed      int
}

func (t *topCmd) getLoadAverage() (*loadAverage, error) {
	f, err := os.Open(filepath.Join(t.rootPath, "/proc/loadavg"))
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc/loadavg: %w", err)
	}
	defer f.Close()
	// 0.02 0.02 0.00 2/442 2990365
	var avg loadAverage
	n, err := fmt.Fscanf(f, "%f %f %f %d/%d %d", &avg.oneMin, &avg.fiveMin, &avg.tenMin, &avg.runningProcesses, &avg.totalProcesses, &avg.lastPidUsed)
	if err != nil {
		return nil, fmt.Errorf("failed to read loadavg: %w", err)
	}
	if n != 6 {
		return nil, fmt.Errorf("failed to read loadavg, invalid number of fields: %d", n)
	}
	return &avg, nil
}

func (t *topCmd) getUptime() (string, error) {
	f, err := os.Open(filepath.Join(t.rootPath, "/proc/uptime"))
	if err != nil {
		return "", fmt.Errorf("unable to read /proc/uptime: %w", err)
	}
	defer f.Close()
	var up, idle float64
	if n, err := fmt.Fscanf(f, "%f %f", &up, &idle); err != nil || n != 2 {
		return "", fmt.Errorf("unable to read /proc/uptime (read %d), %w", n, err)
	}

	const secsPerMin = 60
	const secsPerHour = 60 * secsPerMin
	const secsPerDay = secsPerHour * 24

	upDays := int(up / secsPerDay)
	if upDays > 0 {
		up -= float64(upDays * secsPerDay)
	}
	upHours := int(up / secsPerHour)
	if upHours > 0 {
		up -= float64(upHours * secsPerHour)
	}
	upMinutes := int(up / secsPerMin)

	var uptime string
	if upDays > 0 {
		uptime += fmt.Sprintf(" %d days, %02d:%02d", upDays, upHours, upMinutes)
	} else if upHours > 0 {
		uptime += fmt.Sprintf(" %02d:%02d", upHours, upMinutes)
	} else {
		uptime += fmt.Sprintf(" %02d min", upMinutes)
	}
	return uptime, nil
}
