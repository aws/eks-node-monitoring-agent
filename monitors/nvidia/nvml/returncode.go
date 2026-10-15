package nvml

import (
	"strconv"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Outcome is what a check does with the result of one NVML call.
type Outcome int

const (
	// Observed means the call succeeded and its value is a reading.
	Observed Outcome = iota
	// NotObserved means the GPU or driver does not report the value, so
	// nothing is classified.
	NotObserved
	// FunctionMissing means the loaded driver lacks the call. Each signal
	// decides what that means; unless it says otherwise, it is NotObserved.
	FunctionMissing
	// GPULost means the GPU has fallen off the bus. The cycle reports one
	// lost-GPU signal for it and treats its other reads as not observed.
	GPULost
	// Unexpected is any other error. It is not observed, and it is counted
	// in the nvml_unexpected_returns_total metric.
	Unexpected
)

// outcome maps the return code of an NVML call to an Outcome. No return code
// becomes a hardware state: a read that fails is never classified as a fault.
// DCGM stores sentinel values in the field instead, and NMA has read those as
// real states.
func outcome(call string, ret gonvml.Return) Outcome {
	switch ret {
	case gonvml.SUCCESS:
		return Observed
	case gonvml.ERROR_NOT_SUPPORTED:
		return NotObserved
	case gonvml.ERROR_FUNCTION_NOT_FOUND:
		return FunctionMissing
	case gonvml.ERROR_GPU_IS_LOST:
		return GPULost
	default:
		unexpectedReturns.WithLabelValues(call, strconv.Itoa(int(ret))).Inc()
		return Unexpected
	}
}

// newerSymbols lists the NVML functions that the oldest driver NMA supports
// (R535) does not export. go-nvml binds symbols lazily, so calling a function
// the loaded library lacks aborts the process instead of returning an error.
// Check every name here with symbols.Has before calling it, and treat a
// missing one as ERROR_FUNCTION_NOT_FOUND.
var newerSymbols = []string{
	"nvmlDeviceGetGpuFabricInfoV",
}

// symbols records which of newerSymbols the loaded library exports. The
// lookup runs once, when NVML initializes.
type symbols map[string]bool

func lookupSymbols(ext gonvml.ExtendedInterface, names []string) symbols {
	s := make(symbols, len(names))
	for _, name := range names {
		s[name] = ext.LookupSymbol(name) == nil
	}
	return s
}

// Has reports whether the loaded library exports name. A name that was never
// looked up reports false, so a call missing from newerSymbols fails closed.
func (s symbols) Has(name string) bool {
	return s[name]
}

var (
	libraryLoaded = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvml_library_loaded",
		Help: "1 when the agent has loaded libnvidia-ml, 0 when no candidate path loads.",
	})
	unexpectedReturns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvml_unexpected_returns_total",
		Help: "NVML calls that returned an error code the reader has no rule for, by call and nvmlReturn_t value.",
	}, []string{"call", "return"})
)

func init() {
	metrics.Registry.MustRegister(libraryLoaded, unexpectedReturns)
}
