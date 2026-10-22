// Package nvml reads GPU health from NVML, the NVIDIA driver's management
// library, for the nvidia monitor. It is the replacement for the DCGM
// hostengine connection in package dcgm, and it feeds the same
// classify.NormalizedSignals.
package nvml

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

// Config configures a Reader.
type Config struct {
	// GracePeriod is how long after the reader is created a failed Init
	// stays silent. The nvidia monitor passes DCGM_GRACE_PERIOD_DURATION.
	GracePeriod time.Duration
}

// readyLibrary is what checks need once nvmlInit has succeeded. It is set
// once and never changed.
type readyLibrary struct {
	lib     gonvml.Interface
	symbols newerSymbols
	// gpus is what Init recorded about each GPU, by NVML index. NVML can't
	// be asked about a GPU after it falls off the bus, so conditions name
	// GPUs from here.
	gpus []gpuIdentity
}

// gpuIdentity names one GPU across reboots and replacements, unlike its
// index. A field is empty when NVML didn't report it; some GPUs don't report
// a serial.
type gpuIdentity struct {
	uuid   string
	serial string
}

// readGPUIdentities reads every GPU's UUID and serial. It runs right after
// Init, under the same run.
func readGPUIdentities(lib gonvml.Interface) []gpuIdentity {
	count, ret := lib.DeviceGetCount()
	if outcome("nvmlDeviceGetCount", ret) != Observed {
		return nil
	}
	out := make([]gpuIdentity, count)
	for i := range out {
		dev, ret := lib.DeviceGetHandleByIndex(i)
		if outcome("nvmlDeviceGetHandleByIndex", ret) != Observed {
			continue
		}
		if uuid, ret := dev.GetUUID(); outcome("nvmlDeviceGetUUID", ret) == Observed {
			out[i].uuid = uuid
		}
		if serial, ret := dev.GetSerial(); outcome("nvmlDeviceGetSerial", ret) == Observed {
			out[i].serial = serial
		}
	}
	return out
}

// Reader owns the NVML library handle and its initialization.
type Reader struct {
	gracePeriod time.Duration
	started     time.Time
	now         func() time.Time
	candidates  func() []string
	newLibrary  func(path string) gonvml.Interface

	// initCheck bounds nvmlInit, which can hang in the driver like any
	// other call. It is not one of checks: a hung Init is reported on its
	// own, and the checks only run after Init succeeds.
	initCheck       *Check
	libraryWarnings *renotify.Tracker[string]
	stuckWarnings   *renotify.Tracker[string]

	// probe bounds the driver probe that escalate runs while the checks
	// that read the GPUs are stalled. It is not one of checks, so a stuck
	// probe holds one more thread and never counts toward the stall.
	probe          *Check
	memoryPressure func() (float64, error)

	// probeStuckSince is when the probe's current stuck call started, zero
	// while the probe answers. held is why the last escalation was held at
	// Warning, empty when it wasn't. Only Reconcile touches them.
	probeStuckSince time.Time
	held            string

	// lib and path are only touched by Reconcile.
	lib  gonvml.Interface
	path string

	// ready is set once by Reconcile when Init succeeds, and read by checks
	// through Library. It is nil until then.
	ready atomic.Pointer[readyLibrary]

	// checks must all be registered before the first Reconcile.
	checks []*Check
}

// NewReader returns a Reader that loads libnvidia-ml from the host.
func NewReader(cfg Config) *Reader {
	return newReader(cfg, time.Now, libraryCandidates, func(path string) gonvml.Interface {
		return gonvml.New(gonvml.WithLibraryPath(path))
	})
}

func newReader(cfg Config, now func() time.Time, candidates func() []string, newLibrary func(string) gonvml.Interface) *Reader {
	return &Reader{
		gracePeriod:     cfg.GracePeriod,
		started:         now(),
		now:             now,
		candidates:      candidates,
		newLibrary:      newLibrary,
		initCheck:       newCheck("nvmlInit", DefaultTimeout, now),
		libraryWarnings: renotify.New[string](classify.WarningReNotifyInterval),
		stuckWarnings:   renotify.New[string](classify.WarningReNotifyInterval),
		probe:           newCheck("driverProbe", DefaultTimeout, now),
		memoryPressure:  hostMemoryPressure,
	}
}

// NewCheck registers a check that reads the GPUs and runs one NVML call at a
// time with the given timeout. When every registered check has had a call in
// the driver for stallDuration, Reconcile reports the driver as not
// responding. Register only checks whose runs read every GPU, so that one
// wedged GPU stalls them all. Register every check before the first call to
// Reconcile, which reads the list without a lock.
func (r *Reader) NewCheck(name string, timeout time.Duration) *Check {
	c := newCheck(name, timeout, r.now)
	r.checks = append(r.checks, c)
	return c
}

// Library returns the initialized NVML library and the newer symbols it
// exports. ok is false until Init has succeeded; checks skip their run then.
func (r *Reader) Library() (lib gonvml.Interface, syms newerSymbols, ok bool) {
	s := r.ready.Load()
	if s == nil {
		return nil, newerSymbols{}, false
	}
	return s.lib, s.symbols, true
}

// gpuIdentities returns what Init recorded about each GPU, by index. Call it
// only after Library reports ok.
func (r *Reader) gpuIdentities() []gpuIdentity {
	return r.ready.Load().gpus
}

// Reconcile loads and initializes NVML, retrying on each call until Init
// succeeds. The nvidia monitor calls it every 30 seconds, from one goroutine.
// It reports:
//   - No candidate library loads: a rate-limited NvidiaLibraryNotFound
//     Warning, never a condition. A missing library is an agent packaging
//     problem that replacing the node does not fix.
//   - Init returns an error past the grace period: Fatal DCGMError, the
//     reason package dcgm reports when it cannot reach the hostengine.
//   - Init, or every check that reads the GPUs, with a call in the driver
//     for stallDuration: a rate-limited NvidiaDriverNotResponding Warning.
//     A call that has not returned is not by itself evidence of a GPU
//     fault: heavy load or another agent on the same driver lock can hold
//     it, and repairing the node would end the workload on it.
//   - After Init, the checks stalled and a light driver probe stuck for
//     escalationWindow while host memory is not under pressure: Fatal
//     NvidiaDriverUnresponsive. A driver that answers nothing for that long
//     can't run GPU work, and a reboot clears it. See escalate.
func (r *Reader) Reconcile(ctx context.Context) []monitor.Condition {
	if r.ready.Load() != nil {
		stalled := stalledFor(r.checks, r.now(), stallDuration)
		conds := r.notResponding(stalled,
			fmt.Sprintf("the NVML checks that read the GPUs have each had a call in the NVIDIA driver for at least %v, so GPU health is not being read", stallDuration))
		return append(conds, r.escalate(ctx, stalled)...)
	}

	if r.lib == nil {
		type opened struct {
			lib  gonvml.Interface
			path string
			init initResult
		}
		o, status := run(r.initCheck, func() opened {
			lib, path, ret := openLibrary(r.candidates(), r.newLibrary)
			return opened{lib, path, afterInit(lib, ret)}
		})
		if status == Stuck {
			return r.initStuck()
		}
		if o.lib == nil {
			libraryLoaded.Set(0)
			return r.libraryNotFound()
		}
		libraryLoaded.Set(1)
		r.libraryWarnings.Reset()
		log.FromContext(ctx).Info("loaded NVIDIA management library", "path", o.path)
		r.lib, r.path = o.lib, o.path
		return r.recordInit(ctx, o.init)
	}

	lib := r.lib
	res, status := run(r.initCheck, func() initResult { return afterInit(lib, lib.Init()) })
	if status == Stuck {
		return r.initStuck()
	}
	return r.recordInit(ctx, res)
}

// initResult is the outcome of one Init, and the GPU identities read right
// after a successful one.
type initResult struct {
	ret  gonvml.Return
	gpus []gpuIdentity
}

// afterInit reads the GPU identities when Init succeeded. It runs inside the
// Init run, so a driver that hangs here is bounded like Init itself.
func afterInit(lib gonvml.Interface, ret gonvml.Return) initResult {
	if ret != gonvml.SUCCESS {
		return initResult{ret: ret}
	}
	return initResult{ret: ret, gpus: readGPUIdentities(lib)}
}

// recordInit records the result of Init on the loaded library.
func (r *Reader) recordInit(ctx context.Context, res initResult) []monitor.Condition {
	ret := res.ret
	if ret == gonvml.SUCCESS {
		r.ready.Store(&readyLibrary{lib: r.lib, symbols: lookupNewerSymbols(r.lib.Extensions()), gpus: res.gpus})
		log.FromContext(ctx).Info("initialized NVML", "path", r.path)
		return nil
	}
	if r.inGracePeriod() {
		return nil
	}
	return []monitor.Condition{reasons.DCGMError.Builder().
		Message(fmt.Sprintf("failed to initialize NVML: %v", ret)).
		Build()}
}

// initStuck reports an Init that has been in the driver for stallDuration.
// Shorter hangs, and hangs inside the grace period, stay silent.
func (r *Reader) initStuck() []monitor.Condition {
	return r.notResponding(!r.inGracePeriod() && stalledFor([]*Check{r.initCheck}, r.now(), stallDuration),
		fmt.Sprintf("NVML initialization has been in the NVIDIA driver for at least %v, so GPU health is not being read", stallDuration))
}

// notResponding reports a driver that has stopped answering as a Warning, at
// most once per re-notify interval, and sets the nvml_driver_not_responding
// gauge. Once the driver answers again, the gauge clears and the next stall
// is reported right away.
func (r *Reader) notResponding(stuck bool, msg string) []monitor.Condition {
	if !stuck {
		driverNotResponding.Set(0)
		r.stuckWarnings.Reset()
		return nil
	}
	driverNotResponding.Set(1)
	const key = "driver-not-responding"
	now := r.now()
	if !r.stuckWarnings.ShouldNotify(key, now) {
		return nil
	}
	r.stuckWarnings.Notified(key, now)
	return []monitor.Condition{reasons.NvidiaDriverNotResponding.Builder().Message(msg).Build()}
}

// libraryNotFound reports a missing library once per re-notify interval,
// after the grace period. GPU Operator installs the library some time after
// boot, so the grace period keeps a normal boot quiet.
func (r *Reader) libraryNotFound() []monitor.Condition {
	if r.inGracePeriod() {
		return nil
	}
	const key = "library-not-found"
	now := r.now()
	if !r.libraryWarnings.ShouldNotify(key, now) {
		return nil
	}
	r.libraryWarnings.Notified(key, now)
	return []monitor.Condition{reasons.NvidiaLibraryNotFound.Builder().
		Message(fmt.Sprintf("could not load %s from the host, so GPU health is not being monitored", libraryName)).
		Build()}
}

func (r *Reader) inGracePeriod() bool {
	return r.now().Before(r.started.Add(r.gracePeriod))
}
