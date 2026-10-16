// Package nvml reads GPU health from NVML, the NVIDIA driver's management
// library, for the nvidia monitor. It is the replacement for the DCGM
// hostengine connection in package dcgm, and it feeds the same
// classify.NormalizedSignals.
package nvml

import (
	"context"
	"fmt"
	"sync"
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

	mu      sync.Mutex
	lib     gonvml.Interface
	path    string
	ready   bool
	symbols symbols
	checks  []*Check
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
// consecutive runs, Reconcile reports the driver as hung.
func (r *Reader) NewCheck(name string, timeout time.Duration) *Check {
	c := &Check{name: name, timeout: timeout}
	r.mu.Lock()
	r.checks = append(r.checks, c)
	r.mu.Unlock()
	return c
}

// Library returns the initialized NVML library and the newer symbols it
// exports. ok is false until Init has succeeded; checks skip their run then.
func (r *Reader) Library() (lib gonvml.Interface, syms symbols, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lib, r.symbols, r.ready
}

// Reconcile loads and initializes NVML, retrying on each call until Init
// succeeds. The nvidia monitor calls it every 30 seconds. Its conditions
// follow the parity map:
//   - No candidate library loads: a rate-limited NvidiaLibraryNotFound
//     Warning, never a condition (M2).
//   - Init fails or hangs past the grace period: Fatal DCGMError, as a failed
//     hostengine connection is on main.
//   - After Init, every check stuck for stuckThreshold runs: Fatal DCGMError.
func (r *Reader) Reconcile(ctx context.Context) []monitor.Condition {
	logger := log.FromContext(ctx)

	r.mu.Lock()
	lib, ready, checks := r.lib, r.ready, r.checks
	r.mu.Unlock()

	if ready {
		if allStuck(checks) {
			return []monitor.Condition{reasons.DCGMError.Builder().
				Message(fmt.Sprintf("every NVML check has been stuck in the NVIDIA driver for %d consecutive runs", stuckThreshold)).
				Build()}
		}
		return nil
	}

	if lib == nil {
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
		logger.Info("loaded NVIDIA management library", "path", o.path)
		r.mu.Lock()
		r.lib, r.path = o.lib, o.path
		r.mu.Unlock()
		return r.initialized(ctx, o.lib, o.ret)
	}

	ret, status := run(r.initCheck, lib.Init)
	if status == Stuck {
		return r.initStuck()
	}
	return r.initialized(ctx, lib, ret)
}

// initialized records the result of Init on a loaded library.
func (r *Reader) initialized(ctx context.Context, lib gonvml.Interface, ret gonvml.Return) []monitor.Condition {
	if ret == gonvml.SUCCESS {
		syms := lookupSymbols(lib.Extensions(), newerSymbols)
		r.mu.Lock()
		r.ready = true
		r.symbols = syms
		path := r.path
		r.mu.Unlock()
		log.FromContext(ctx).Info("initialized NVML", "path", path)
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
