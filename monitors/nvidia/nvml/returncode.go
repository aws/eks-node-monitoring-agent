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

// newerSymbols records which NVML functions newer than R535, the oldest
// driver NMA supports, the loaded library exports. go-nvml binds symbols
// lazily, so calling a function the library lacks aborts the process instead
// of returning an error. Check the matching field before calling one, and
// treat false as ERROR_FUNCTION_NOT_FOUND. A field without a lookup in
// lookupNewerSymbols fails TestNewerSymbolsAllLookedUp.
type newerSymbols struct {
	gpuFabricInfoV bool // nvmlDeviceGetGpuFabricInfoV
}

// lookupNewerSymbols checks the loaded library for each newer function. It
// runs once, when NVML initializes.
func lookupNewerSymbols(ext gonvml.ExtendedInterface) newerSymbols {
	has := func(name string) bool { return ext.LookupSymbol(name) == nil }
	return newerSymbols{
		gpuFabricInfoV: has("nvmlDeviceGetGpuFabricInfoV"),
	}
}

var (
	libraryLoaded = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvml_library_loaded",
		Help: "1 when the agent has loaded libnvidia-ml, 0 when no candidate path loads.",
	})
	driverNotResponding = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvml_driver_not_responding",
		Help: "1 while NVML initialization, or every NVML check, has been stuck in the NVIDIA driver for the stuck threshold; 0 otherwise.",
	})
	xidUnregisteredGPUs = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvml_xid_unregistered_gpus",
		Help: "GPUs that failed to register for critical XID events and are waiting for a retry. Their XIDs are missed until they register.",
	})
	unexpectedReturns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvml_unexpected_returns_total",
		Help: "NVML calls that returned an error code the reader has no rule for, by call and nvmlReturn_t value.",
	}, []string{"call", "return"})
)

func init() {
	metrics.Registry.MustRegister(libraryLoaded, driverNotResponding, xidUnregisteredGPUs, unexpectedReturns)
}
