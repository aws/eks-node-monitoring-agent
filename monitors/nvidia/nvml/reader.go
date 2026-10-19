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
		initCheck:       &Check{name: "nvmlInit", timeout: DefaultTimeout},
		libraryWarnings: renotify.New[string](classify.WarningReNotifyInterval),
	}
}

// NewCheck registers a check that runs one NVML call at a time with the given
// timeout. When every registered check has been stuck for stuckThreshold
// consecutive runs, Reconcile reports the driver as hung. Register every
// check before the first call to Reconcile, which reads the list without a
// lock.
func (r *Reader) NewCheck(name string, timeout time.Duration) *Check {
	c := &Check{name: name, timeout: timeout}
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

// Reconcile loads and initializes NVML, retrying on each call until Init
// succeeds. The nvidia monitor calls it every 30 seconds, from one goroutine.
// Its conditions follow the parity map:
//   - No candidate library loads: a rate-limited NvidiaLibraryNotFound
//     Warning, never a condition (M2).
//   - Init fails or hangs past the grace period: Fatal DCGMError, as a failed
//     hostengine connection is on main.
//   - After Init, every check stuck for stuckThreshold runs: Fatal DCGMError.
func (r *Reader) Reconcile(ctx context.Context) []monitor.Condition {
	if r.ready.Load() != nil {
		if allStuck(r.checks) {
			return []monitor.Condition{reasons.DCGMError.Builder().
				Message(fmt.Sprintf("every NVML check has been stuck in the NVIDIA driver for %d consecutive runs", stuckThreshold)).
				Build()}
		}
		return nil
	}

	if r.lib == nil {
		type opened struct {
			lib  gonvml.Interface
			path string
			ret  gonvml.Return
		}
		o, status := run(r.initCheck, func() opened {
			lib, path, ret := openLibrary(r.candidates(), r.newLibrary)
			return opened{lib, path, ret}
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
		return r.recordInit(ctx, o.ret)
	}

	ret, status := run(r.initCheck, r.lib.Init)
	if status == Stuck {
		return r.initStuck()
	}
	return r.recordInit(ctx, ret)
}

// recordInit records the result of Init on the loaded library.
func (r *Reader) recordInit(ctx context.Context, ret gonvml.Return) []monitor.Condition {
	if ret == gonvml.SUCCESS {
		r.ready.Store(&readyLibrary{lib: r.lib, symbols: lookupNewerSymbols(r.lib.Extensions())})
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

// initStuck reports an Init that has been in the driver for stuckThreshold
// runs. Shorter hangs, and hangs inside the grace period, stay silent.
func (r *Reader) initStuck() []monitor.Condition {
	if r.inGracePeriod() || r.initCheck.StuckCycles() < stuckThreshold {
		return nil
	}
	return []monitor.Condition{reasons.DCGMError.Builder().
		Message(fmt.Sprintf("NVML initialization has been stuck in the NVIDIA driver for %d consecutive runs", stuckThreshold)).
		Build()}
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
