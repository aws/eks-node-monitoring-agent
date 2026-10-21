package nvml

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// escalationWindow is how long the driver probe must stay in the driver,
// after the checks that read the GPUs have stalled, before the reader reports
// the driver as unresponsive. A driver that answers nothing for this long
// can't run GPU work, and a reboot clears it.
const escalationWindow = 15 * time.Minute

// maxHostMemoryPressure is the PSI "full avg60" value, in percent, above
// which a stall stays a Warning. Under memory pressure every process on the
// host stalls, the driver included, so the stall says nothing about the GPUs.
const maxHostMemoryPressure = 10.0

// escalate runs the driver probe while the checks that read the GPUs are
// stalled, and reports NvidiaDriverUnresponsive once every probe run for
// escalationWindow has ended Stuck and host memory pressure is known and at
// or below maxHostMemoryPressure. A probe that answers, or checks that
// answer, start the window over. It is only called from Reconcile.
func (r *Reader) escalate(ctx context.Context, stalled bool) []monitor.Condition {
	if !stalled {
		r.probeStuckSince, r.held = time.Time{}, ""
		return nil
	}
	lib, _, _ := r.Library()
	start := r.now()
	if _, status := run(r.probe, func() struct{} { probeDriver(lib); return struct{}{} }); status == Done {
		// The driver answered a light call, so a heavy call is slow, not hung.
		r.probeStuckSince, r.held = time.Time{}, ""
		return nil
	}
	if r.probeStuckSince.IsZero() {
		r.probeStuckSince = start
	}
	if r.now().Sub(r.probeStuckSince) < escalationWindow {
		return nil
	}

	pressure, err := r.memoryPressure()
	var held string
	switch {
	case err != nil:
		held = "host memory pressure could not be read"
	case pressure > maxHostMemoryPressure:
		held = "the host is under memory pressure"
	}
	if held != "" {
		// Logged when the reason changes, not on every run.
		if held != r.held {
			log.FromContext(ctx).Info("NVIDIA driver is not answering, but staying at Warning",
				"reason", held, "memoryPressureFullAvg60", pressure, "error", err)
		}
		r.held = held
		return nil
	}
	r.held = ""
	return []monitor.Condition{reasons.NvidiaDriverUnresponsive.Builder().
		Message(fmt.Sprintf("a light driver read has been stuck in the NVIDIA driver for at least %v after the checks that read the GPUs stopped answering", escalationWindow)).
		Build()}
}

// probeDriver reads every GPU's PCIe replay counter and ignores the result:
// it only has to come back. It reads every GPU, so one wedged GPU keeps it
// stuck. The call reaches the driver on every read, which is the point: a
// hung driver can't answer it. Most other NVML reads can't serve as a probe,
// because the driver keeps a shared-memory page of recent values that NVML
// reads without entering the driver (measured: temperature, power, ECC
// counts, retired pages and utilization all answer from the page, and
// nvmlDeviceGetCount and nvmlDeviceGetHandleByIndex from state built at init),
// so they answer even when the driver has hung. The replay counter, the
// NVLink error fields and the fabric info are read straight from the driver
// on every call.
func probeDriver(lib gonvml.Interface) {
	count, ret := lib.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return
	}
	for i := range count {
		if dev, ret := lib.DeviceGetHandleByIndex(i); ret == gonvml.SUCCESS {
			_, _ = dev.GetPcieReplayCounter()
		}
	}
}

// hostMemoryPressure returns the "full avg60" value from the host's
// /proc/pressure/memory: the share of the last minute, in percent, in which
// every non-idle task was stalled on memory. It fails when the kernel has PSI
// off.
func hostMemoryPressure() (float64, error) {
	data, err := os.ReadFile(config.ToHostPath("/proc/pressure/memory"))
	if err != nil {
		return 0, err
	}
	return fullAvg60(string(data))
}

// fullAvg60 parses the avg60 value of the "full" line of a PSI file.
func fullAvg60(psi string) (float64, error) {
	for _, line := range strings.Split(psi, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "full" {
			continue
		}
		for _, field := range fields[1:] {
			if v, ok := strings.CutPrefix(field, "avg60="); ok {
				return strconv.ParseFloat(v, 64)
			}
		}
	}
	return 0, errors.New(`no "full avg60" value in PSI data`)
}
