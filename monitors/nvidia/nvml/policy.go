package nvml

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

// PolicyInterval is how often Policies.Poll should run. It matches the rate
// at which DCGM samples the fields behind its policies.
const PolicyInterval = 10 * time.Second

// Policy thresholds. On the DCGM path these are policy parameters that NMA
// sets on the hostengine, or keeps if the hostengine already holds them.
// Without a hostengine they are constants.
const (
	maxRetiredPages = 10  // single-bit plus double-bit retired pages
	maxTemperatureC = 100 // GPU core temperature
)

// DCGM field IDs of the counter policies. Baselines are keyed by them, as
// they are in DCGM, and the NVLink message names them.
const (
	dcgmFieldPCIeReplays = 202 // DCGM_FI_DEV_PCIE_REPLAY_TOTAL
	dcgmFieldDBEDevice   = 319 // DCGM_FI_DEV_ECC_DBE_VOL_DEV
)

// nvlinkErrorTotals are the NVML field values behind DCGM's four NVLink
// policy fields. DCGM reads the same NVML fields for them.
var nvlinkErrorTotals = []struct {
	nvmlField uint32
	dcgmField uint16
}{
	{gonvml.FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL, 409}, // DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_TOTAL
	{gonvml.FI_DEV_NVLINK_CRC_DATA_ERROR_COUNT_TOTAL, 419}, // DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_TOTAL
	{gonvml.FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL, 429},   // DCGM_FI_DEV_NVLINK_REPLAY_ERROR_TOTAL
	{gonvml.FI_DEV_NVLINK_RECOVERY_ERROR_COUNT_TOTAL, 439}, // DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_TOTAL
}

// Policies reads the signals DCGM's policies watch and applies the same
// rules: a double-bit ECC, PCIe replay or NVLink error counter that grew since
// the last poll, more than maxRetiredPages retired pages, a temperature above
// maxTemperatureC, and power above the threshold. Like DCGM, it reports each
// policy at most once per GPU per poll, and a counter needs two readings in a
// row before it can report growth. A GPU that answers with ERROR_GPU_IS_LOST
// reports the lost-GPU Fatal and nothing else.
//
// Retired pages, temperature and power are levels, not counters: they would
// report on every poll while the reading stays over its limit. Each reports
// at most once per classify.WarningReNotifyInterval per GPU, and again right
// away after the GPU reads within the limit.
//
// One poll's NVML calls, about nine per GPU, run as a single run of one check
// under DefaultTimeout. At milliseconds per call that is far inside the
// deadline, and one check per poll means a hung driver holds at most one
// thread for all GPUs.
type Policies struct {
	reader *Reader
	check  *Check

	// powerThresholdWatts is the configured power threshold. Zero means each
	// GPU's enforced power limit.
	powerThresholdWatts atomic.Uint32

	now func() time.Time

	// Only the goroutine that calls Poll touches the maps below. baselines
	// holds each counter's last reading.
	baselines map[counterKey]uint64
	notified  levelReports
}

type counterKey struct {
	gpu       int
	dcgmField uint16
}

// levelKey identifies one GPU's level signal for rate limiting.
type levelKey struct {
	gpu    int
	signal string
}

// Level policies, rate-limited per GPU.
const (
	retiredPagesPolicy = "retiredPages"
	thermalPolicy      = "thermal"
	powerPolicy        = "power"
)

// NewPolicies registers the policy checks with r. Create it before the first
// call to r.Reconcile.
func NewPolicies(r *Reader) *Policies {
	return &Policies{
		reader:    r,
		check:     r.NewCheck("policies", DefaultTimeout),
		now:       time.Now,
		baselines: map[counterKey]uint64{},
		notified:  levelReports{},
	}
}

// SetPowerThresholdWatts sets the power threshold, the nvidia monitor's
// dcgmPowerThresholdWatts setting. Zero, the default, compares each GPU
// against its enforced power limit.
func (p *Policies) SetPowerThresholdWatts(watts uint32) {
	p.powerThresholdWatts.Store(watts)
}

// Poll reads every GPU once and returns this poll's policy conditions. ok is
// false when the poll read nothing: NVML is not initialized, the GPU count
// could not be read, or an earlier poll is still stuck in the driver. A poll
// that did not read is unknown, not clean. Call it every PolicyInterval from
// one goroutine.
func (p *Policies) Poll() (conds []monitor.Condition, ok bool) {
	lib, _, ready := p.reader.Library()
	if !ready {
		return nil, false
	}
	readLimit := p.powerThresholdWatts.Load() == 0
	type polled struct {
		gpus []gpuReadings
		ok   bool
	}
	result, status := run(p.check, func() polled {
		gpus, ok := readPolicySignals(lib, readLimit)
		return polled{gpus, ok}
	})
	if status == Stuck || !result.ok {
		// The readings are dropped and the baselines kept, so a counter that
		// grew during the stall is reported by the next poll that answers.
		return nil, false
	}
	return p.evaluate(result.gpus, p.reader.gpuIdentities()), true
}

// reading is the result of one NVML call. ok is false when the value was not
// observed.
type reading struct {
	value uint64
	ok    bool
}

// gpuReadings is one poll's values for one GPU.
type gpuReadings struct {
	lost         bool
	dbe          reading
	pcieReplays  reading
	retiredSBE   reading
	retiredDBE   reading
	temperatureC reading
	powerMW      reading
	powerLimitMW reading
	nvlink       []reading // one per nvlinkErrorTotals entry
}

// readPolicySignals reads every GPU. It runs on the check's goroutine and
// only makes NVML calls; Poll evaluates the readings. ok is false when the
// GPU count could not be read.
func readPolicySignals(lib gonvml.Interface, readLimit bool) ([]gpuReadings, bool) {
	count, ret := lib.DeviceGetCount()
	if outcome("nvmlDeviceGetCount", ret) != Observed {
		return nil, false
	}
	out := make([]gpuReadings, count)
	for i := range out {
		out[i] = readGPU(lib, i, readLimit)
	}
	return out, true
}

// gpuReader applies the return-code policy to one GPU's calls and remembers
// whether the GPU was lost.
type gpuReader struct{ lost bool }

func (r *gpuReader) observe(call string, value uint64, ret gonvml.Return) reading {
	switch outcome(call, ret) {
	case Observed:
		return reading{value: value, ok: true}
	case GPULost:
		r.lost = true
	}
	return reading{}
}

func readGPU(lib gonvml.Interface, index int, readLimit bool) gpuReadings {
	var r gpuReader
	dev, ret := lib.DeviceGetHandleByIndex(index)
	if !r.observe("nvmlDeviceGetHandleByIndex", 0, ret).ok {
		return gpuReadings{lost: r.lost}
	}

	// One step per read, in a fixed order. Each step reads one value and
	// records it whether or not it was observed.
	g := gpuReadings{nvlink: make([]reading, len(nvlinkErrorTotals))}
	steps := []func(){
		// Double-bit policy: uncorrected ECC errors in device memory since
		// the driver loaded. Device memory only, as DCGM's policy reads it;
		// the total also counts caches and register files.
		func() {
			v, ret := dev.GetMemoryErrorCounter(gonvml.MEMORY_ERROR_TYPE_UNCORRECTED, gonvml.VOLATILE_ECC, gonvml.MEMORY_LOCATION_DEVICE_MEMORY)
			g.dbe = r.observe("nvmlDeviceGetMemoryErrorCounter", v, ret)
		},
		// PCIe policy: the GPU's PCIe replay counter.
		func() {
			v, ret := dev.GetPcieReplayCounter()
			g.pcieReplays = r.observe("nvmlDeviceGetPcieReplayCounter", uint64(v), ret)
		},
		// Retired-pages policy, first half: pages retired after repeated
		// single-bit errors. Only the count of the returned list is used.
		func() {
			pages, ret := dev.GetRetiredPages(gonvml.PAGE_RETIREMENT_CAUSE_MULTIPLE_SINGLE_BIT_ECC_ERRORS)
			g.retiredSBE = r.observe("nvmlDeviceGetRetiredPages", uint64(len(pages)), ret)
		},
		// Retired-pages policy, second half: pages retired after a
		// double-bit error.
		func() {
			pages, ret := dev.GetRetiredPages(gonvml.PAGE_RETIREMENT_CAUSE_DOUBLE_BIT_ECC_ERROR)
			g.retiredDBE = r.observe("nvmlDeviceGetRetiredPages", uint64(len(pages)), ret)
		},
		// Thermal policy: GPU core temperature in degrees C.
		func() {
			v, ret := dev.GetTemperature(gonvml.TEMPERATURE_GPU)
			g.temperatureC = r.observe("nvmlDeviceGetTemperature", uint64(v), ret)
		},
		// Power policy: board power draw in milliwatts.
		func() {
			v, ret := dev.GetPowerUsage()
			g.powerMW = r.observe("nvmlDeviceGetPowerUsage", uint64(v), ret)
		},
		// Power policy limit, in milliwatts. Read only when no threshold is
		// configured, because then the GPU's own enforced limit is the bar.
		func() {
			if readLimit {
				v, ret := dev.GetEnforcedPowerLimit()
				g.powerLimitMW = r.observe("nvmlDeviceGetEnforcedPowerLimit", uint64(v), ret)
			}
		},
		// NVLink policy: the four NVLink error totals, summed over links by
		// the driver, in one field-values call. Each value carries its own
		// return code.
		func() {
			values := make([]gonvml.FieldValue, len(nvlinkErrorTotals))
			for i, f := range nvlinkErrorTotals {
				values[i].FieldId = f.nvmlField
			}
			if !r.observe("nvmlDeviceGetFieldValues", 0, dev.GetFieldValues(values)).ok {
				return
			}
			for i, v := range values {
				n, isUint := fieldValueUint(v)
				if got := r.observe("nvmlDeviceGetFieldValues", n, gonvml.Return(v.NvmlReturn)); got.ok && isUint {
					g.nvlink[i] = got
				}
			}
		},
	}
	// A lost GPU answers every call with ERROR_GPU_IS_LOST, so stop at the
	// first one.
	for _, step := range steps {
		step()
		if r.lost {
			return gpuReadings{lost: true}
		}
	}
	return g
}

// fieldValueUint decodes a non-negative integer NVML field value. NVML writes
// the value in the host's byte order and reports its integer type with it, so
// every integer type is accepted rather than reading a count as not observed
// because of its encoding. A negative signed value is not a count, and a
// double is not an integer, so neither is observed.
func fieldValueUint(v gonvml.FieldValue) (uint64, bool) {
	switch gonvml.ValueType(v.ValueType) {
	case gonvml.VALUE_TYPE_UNSIGNED_SHORT:
		return uint64(binary.NativeEndian.Uint16(v.Value[:2])), true
	case gonvml.VALUE_TYPE_UNSIGNED_INT:
		return uint64(binary.NativeEndian.Uint32(v.Value[:4])), true
	case gonvml.VALUE_TYPE_UNSIGNED_LONG, gonvml.VALUE_TYPE_UNSIGNED_LONG_LONG:
		return binary.NativeEndian.Uint64(v.Value[:]), true
	case gonvml.VALUE_TYPE_SIGNED_INT:
		if n := int32(binary.NativeEndian.Uint32(v.Value[:4])); n >= 0 {
			return uint64(n), true
		}
	case gonvml.VALUE_TYPE_SIGNED_LONG_LONG:
		if n := int64(binary.NativeEndian.Uint64(v.Value[:])); n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

// evaluate applies the policy rules to one poll's readings and updates the
// counter baselines. Messages match the DCGM path's.
func (p *Policies) evaluate(samples []gpuReadings, gpus []gpuIdentity) []monitor.Condition {
	var out []monitor.Condition
	for gpu, s := range samples {
		if s.lost {
			// A lost GPU's values are not observed, so its counters need two
			// new readings before they can report growth.
			p.clearBaselines(gpu)
			out = append(out, lostGPU(gpu, gpus)...)
			continue
		}

		if p.grew(counterKey{gpu, dcgmFieldDBEDevice}, s.dbe) {
			out = append(out, withMessage(classify.NormalizedSignals{DoubleBitECC: true},
				fmt.Sprintf("detected %d Nvidia Double Bit error(s) on location Device", s.dbe.value))...)
		}

		// Every counter updates its baseline, but the policy reports the
		// first one that grew.
		reported := false
		for i, r := range s.nvlink {
			if p.grew(counterKey{gpu, nvlinkErrorTotals[i].dcgmField}, r) && !reported {
				out = append(out, withMessage(classify.NormalizedSignals{NVLinkError: true},
					fmt.Sprintf("detected %d NVLink errors on fieldId %v", r.value, nvlinkErrorTotals[i].dcgmField))...)
				reported = true
			}
		}

		pagesObserved := s.retiredSBE.ok && s.retiredDBE.ok
		if p.notified.report(p.now(), levelKey{gpu, retiredPagesPolicy}, pagesObserved, s.retiredSBE.value+s.retiredDBE.value > maxRetiredPages) {
			out = append(out, withMessage(classify.NormalizedSignals{PageRetirement: true},
				fmt.Sprintf("detected %d SBE, and %d DBE page retirements", s.retiredSBE.value, s.retiredDBE.value))...)
		}

		if p.notified.report(p.now(), levelKey{gpu, thermalPolicy}, s.temperatureC.ok, s.temperatureC.value > maxTemperatureC) {
			out = append(out, withMessage(classify.NormalizedSignals{ThermalViolation: true},
				fmt.Sprintf("detected GPU thermals outside of thresholds with severity code %d", s.temperatureC.value))...)
		}

		watts, powerObserved, overPower := p.power(s)
		if p.notified.report(p.now(), levelKey{gpu, powerPolicy}, powerObserved, overPower) {
			out = append(out, withMessage(classify.NormalizedSignals{PowerViolation: true},
				fmt.Sprintf("detected power usage outside of thresholds with severity code %d", watts))...)
		}

		if p.grew(counterKey{gpu, dcgmFieldPCIeReplays}, s.pcieReplays) {
			out = append(out, withMessage(classify.NormalizedSignals{PCIeReplay: true},
				fmt.Sprintf("detected %d PCIe replays", s.pcieReplays.value))...)
		}
	}
	return out
}

// grew records r as the counter's baseline and reports whether it grew since
// the previous reading. A missing reading clears the baseline, as a blank
// sample does in DCGM. So does a value above MaxUint32, which DCGM's counter
// policies treat as out of range. A drop is a counter reset and does not
// report.
func (p *Policies) grew(key counterKey, r reading) bool {
	prev, had := p.baselines[key]
	if !r.ok || r.value > math.MaxUint32 {
		delete(p.baselines, key)
		return false
	}
	p.baselines[key] = r.value
	return had && r.value > prev
}

func (p *Policies) clearBaselines(gpu int) {
	for key := range p.baselines {
		if key.gpu == gpu {
			delete(p.baselines, key)
		}
	}
}

// levelReports rate-limits Warnings for signals that can stay over their
// limit for many polls. It holds a re-notify tracker for each GPU and signal
// that has reported, and only for those. Only the goroutine that polls
// touches it.
type levelReports map[levelKey]*renotify.Tracker[struct{}]

// report reports whether a level signal should report at now. A reading over
// the limit reports at most once per WarningReNotifyInterval per GPU and
// signal. A reading within the limit drops the tracker, so the next violation
// reports at once. A reading that was not observed changes nothing, because
// unknown is not clean.
//
// The report is recorded here, not after it is sent, because the poll's
// caller sends it later and nothing here learns whether that worked.
func (l levelReports) report(now time.Time, key levelKey, observed, over bool) bool {
	if !observed {
		return false
	}
	if !over {
		delete(l, key)
		return false
	}
	tracker, tracked := l[key]
	if tracked && !tracker.ShouldNotify(struct{}{}, now) {
		return false
	}
	if !tracked {
		tracker = renotify.New[struct{}](classify.WarningReNotifyInterval)
		l[key] = tracker
	}
	tracker.Notified(struct{}{}, now)
	return true
}

// power compares whole watts against the threshold, as DCGM does. With no
// threshold configured, it uses the GPU's enforced power limit. observed is
// false when the draw or the limit it needs could not be read.
func (p *Policies) power(s gpuReadings) (watts uint64, observed, over bool) {
	if !s.powerMW.ok {
		return 0, false, false
	}
	limit := uint64(p.powerThresholdWatts.Load())
	if limit == 0 {
		if !s.powerLimitMW.ok {
			return 0, false, false
		}
		limit = s.powerLimitMW.value / 1000
	}
	watts = s.powerMW.value / 1000
	return watts, true, watts > limit
}

// withMessage classifies s and replaces each condition's message with msg.
func withMessage(s classify.NormalizedSignals, msg string) []monitor.Condition {
	conds := classify.Classify(s)
	for i := range conds {
		conds[i].Message = msg
	}
	return conds
}
