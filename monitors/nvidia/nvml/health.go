package nvml

import (
	"fmt"
	"math"
	"strconv"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// HealthInterval is how often Health.Poll should run. It matches the DCGM
// reader's health check.
const HealthInterval = 5 * time.Minute

// inforomInterval is how often Health validates each GPU's InfoROM. DCGM
// validates it at most hourly, and reports the last result in between.
const inforomInterval = time.Hour

// Health limits, the values DCGM uses (common/DcgmGPUHardwareLimits.h and
// common/DcgmUtilities.cpp).
const (
	maxRetiredPagesTotal   = 63  // single-bit plus double-bit retired pages
	maxRetiredPagesDBE     = 15  // pages retired after double-bit errors
	maxUncorrectableRemaps = 512 // uncorrectable row remaps, Ampere and newer
	ampereComputeMajor     = 8   // first CUDA compute capability with row remapping
	blackwellComputeMajor  = 10  // first CUDA compute capability of Blackwell

	// An NVLink BER is packed as mantissa x 10^-exponent. Mantissa 15 with
	// exponent 255 encodes a BER of zero.
	berZeroMantissa = 15
	berZeroExponent = 255
	// Effective BER fails above 1e-12.
	effectiveBERExponent = 12
	// Symbol BER fails when its exponent is below the floor, so at
	// 1e-(floor-1) and above. Blackwell and newer report it summed over
	// links, with a lower floor.
	symbolBERFloorExponent       = 255
	symbolBERFloorExponentSummed = 238
)

// DCGM health codes (dcgm_errors.h) that Health reports.
const (
	codeVolatileDBE          = 4
	codePendingRetirements   = 6
	codeRetiredPagesLimit    = 7
	codeRetiredPagesDBELimit = 8
	codeCorruptInforom       = 9
	codeClocksThermal        = 10
	codePowerUnreadable      = 11
	codeClocksPower          = 12
	codeNVLinkDown           = 14
	codeFaultyMemory         = 52
	codeNVLinkErrorCritical  = 71
	codeRowRemapFailure      = 80
	codeEffectiveBER         = 119
	codeSymbolBER            = 121
	codeRecoveryReset        = 125
	codeRecoveryReboot       = 126
	codeRecoveryDrainP2P     = 127
	codeRecoveryDrainReset   = 128
	codeUncorrectableRemaps  = 132
)

// Health reports the DCGM health-watch codes, as DCGMHealthCode<N> with
// DCGM's verdict: FAIL is Fatal, WARN a Warning. Each rule reads the value
// DCGM reads for it (DcgmHealthWatch.cpp, DCGM 4.6.1):
//
//	Code  Rule                                                NVML source                          Severity
//	4     uncorrected volatile ECC errors > 0, any location   field ECC_DBE_VOL_TOTAL              Fatal
//	6     pages pending retirement                            field RETIRED_PENDING                Warning
//	7     single-bit plus double-bit retired pages >= 63      fields RETIRED_SBE, RETIRED_DBE      Fatal
//	8     double-bit retired pages > 15, unless 7 fired       field RETIRED_DBE                    Fatal
//	9     InfoROM corrupted, checked at most hourly           nvmlDeviceValidateInforom            Warning
//	10    thermal slowdown time grew                          field PERF_POLICY_THERMAL            Warning
//	11    power can't be read                                 nvmlDeviceGetPowerUsage              Warning
//	12    power slowdown time grew                            field PERF_POLICY_POWER              Warning
//	14    an NVLink is down, outside MIG mode                 nvmlDeviceGetNvLinkState             Fatal
//	52    unrepairable memory                                 nvmlDeviceGetUnrepairableMemoryFlag  Fatal
//	71    an NVLink5 error counter grew                       fields NVLINK_COUNT_*, NVLINK_ECC    Fatal
//	71    Blackwell: an NVLink recovery failed                field RECOVERY_FAILED_EVENTS         Fatal
//	71    Blackwell: an NVLink recovery succeeded             field RECOVERY_SUCCESSFUL_EVENTS     Warning
//	80    row remapping failed                                field REMAPPED_FAILURE               Fatal
//	119   NVLink effective BER above 1e-12                    field NVLINK_COUNT_EFFECTIVE_BER     Fatal
//	121   NVLink symbol BER exponent below its floor          field NVLINK_COUNT_SYMBOL_BER        Fatal
//	125   recovery action: reset the GPU                      field GET_GPU_RECOVERY_ACTION        Fatal
//	126   recovery action: reboot the node                    field GET_GPU_RECOVERY_ACTION        Fatal
//	127   recovery action: drain peer-to-peer traffic         field GET_GPU_RECOVERY_ACTION        Warning
//	128   recovery action: drain and reset the GPU            field GET_GPU_RECOVERY_ACTION        Fatal
//	132   uncorrectable row remaps >= 512, Ampere and newer   field REMAPPED_UNC                   Warning
//
// Codes 10, 12 and 71's counters compare each reading with the previous
// poll's, so they cover the whole interval between polls. DCGM compares the
// first and last samples of the last minute, and misses a change earlier in
// the interval. As for the policy counters, the first reading is a baseline,
// a failed read clears it, and a drop is a counter reset that doesn't report.
//
// Code 14 skips a GPU in MIG mode, and counts no link as down while any GPU on
// the node is in MIG mode, because NVML reports links of MIG GPUs as down. It
// checks only the links the GPU reports in NVLINK_LINK_COUNT, and a link
// whose state can't be read is skipped.
//
// Code 8: DCGM also asks for more than one new page in the past week, but the
// health watch keeps 10 minutes of samples, so the week-old sample is never
// there and DCGM reports on the count alone. Health reports what DCGM
// reports. Recovery actions above 4 (recover the IMEX domain, reset the PCIe
// bus, reboot the system) are newer than DCGM 4.6.1, which ignores them, so
// Health ignores them too.
//
// Warnings report at most once per WarningReNotifyInterval per GPU and code,
// and again right away after the GPU reads clean. Fatals report on every poll.
//
// Not ported, because another check already reports the signal: PCIe replay
// rate (3), the NVLink4 error counters (13, 70, and 71's NVLink4 and lifetime
// counters), fabric fields (33, 123), and the XID-derived codes. IMEX (122)
// stays skipped.
//
// One poll reads every GPU as a single run of one check under DefaultTimeout.
type Health struct {
	reader *Reader
	check  *Check
	now    func() time.Time

	// Only the goroutine that calls Poll touches the fields below.
	// inforomAt is when the InfoROMs were last validated, and inforom holds
	// each GPU's result until the next validation. baselines holds each
	// rate counter's last reading.
	inforomAt time.Time
	inforom   []reading
	baselines map[healthCounterKey]uint64
	notified  levelReports
}

// healthCounterKey identifies one GPU's rate counter, by its index into
// healthFields.
type healthCounterKey struct {
	gpu   int
	field int
}

// NewHealth registers the health check with r. It reads every GPU, so it
// counts toward the stall. Create it before the first call to r.Reconcile.
func NewHealth(r *Reader) *Health {
	return &Health{
		reader:    r,
		check:     r.NewCheck("health", DefaultTimeout),
		now:       time.Now,
		baselines: map[healthCounterKey]uint64{},
		notified:  levelReports{},
	}
}

// Indexes into healthFields and gpuHealth.fields.
const (
	fieldDBEVolatile = iota
	fieldRetiredPending
	fieldRetiredSBE
	fieldRetiredDBE
	fieldRemapFailure
	fieldRemapUncorrectable
	fieldRecoveryAction
	fieldThermalViolation
	fieldPowerViolation
	fieldNVLinkCount
	fieldNVLinkSymbolErrors
	fieldNVLinkMalformedPackets
	fieldNVLinkBufferOverruns
	fieldNVLinkReceiveErrors
	fieldNVLinkRemoteErrors
	fieldNVLinkGeneralErrors
	fieldNVLinkIntegrityErrors
	fieldNVLinkRecoveryEvents
	fieldNVLinkEffectiveErrors
	fieldNVLinkECCErrors
	fieldNVLinkEffectiveBER
	fieldNVLinkSymbolBER
	fieldNVLinkRecoverySucceeded
	fieldNVLinkRecoveryFailed
	numHealthFields
)

// healthField is one NVML field value Health reads. nvlink5 marks the NVLink5
// fields, which Blackwell and newer GPUs report summed over links only when
// asked for scope MaxUint32. DCGM asks for that scope on those GPUs and for
// scope 0 on older ones, and Health does the same.
type healthField struct {
	id      uint32
	nvlink5 bool
}

// healthFields are the NVML field values Health reads, in one
// nvmlDeviceGetFieldValues call per GPU.
var healthFields = [numHealthFields]healthField{
	fieldDBEVolatile:             {id: gonvml.FI_DEV_ECC_DBE_VOL_TOTAL},
	fieldRetiredPending:          {id: gonvml.FI_DEV_RETIRED_PENDING},
	fieldRetiredSBE:              {id: gonvml.FI_DEV_RETIRED_SBE},
	fieldRetiredDBE:              {id: gonvml.FI_DEV_RETIRED_DBE},
	fieldRemapFailure:            {id: gonvml.FI_DEV_REMAPPED_FAILURE},
	fieldRemapUncorrectable:      {id: gonvml.FI_DEV_REMAPPED_UNC},
	fieldRecoveryAction:          {id: gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION},
	fieldThermalViolation:        {id: gonvml.FI_DEV_PERF_POLICY_THERMAL},
	fieldPowerViolation:          {id: gonvml.FI_DEV_PERF_POLICY_POWER},
	fieldNVLinkCount:             {id: gonvml.FI_DEV_NVLINK_LINK_COUNT},
	fieldNVLinkSymbolErrors:      {id: gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_ERRORS, nvlink5: true},
	fieldNVLinkMalformedPackets:  {id: gonvml.FI_DEV_NVLINK_COUNT_MALFORMED_PACKET_ERRORS, nvlink5: true},
	fieldNVLinkBufferOverruns:    {id: gonvml.FI_DEV_NVLINK_COUNT_BUFFER_OVERRUN_ERRORS, nvlink5: true},
	fieldNVLinkReceiveErrors:     {id: gonvml.FI_DEV_NVLINK_COUNT_RCV_ERRORS, nvlink5: true},
	fieldNVLinkRemoteErrors:      {id: gonvml.FI_DEV_NVLINK_COUNT_RCV_REMOTE_ERRORS, nvlink5: true},
	fieldNVLinkGeneralErrors:     {id: gonvml.FI_DEV_NVLINK_COUNT_RCV_GENERAL_ERRORS, nvlink5: true},
	fieldNVLinkIntegrityErrors:   {id: gonvml.FI_DEV_NVLINK_COUNT_LOCAL_LINK_INTEGRITY_ERRORS, nvlink5: true},
	fieldNVLinkRecoveryEvents:    {id: gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_EVENTS, nvlink5: true},
	fieldNVLinkEffectiveErrors:   {id: gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_ERRORS, nvlink5: true},
	fieldNVLinkECCErrors:         {id: gonvml.FI_DEV_NVLINK_ECC_DATA_ERROR_COUNT_TOTAL},
	fieldNVLinkEffectiveBER:      {id: gonvml.FI_DEV_NVLINK_COUNT_EFFECTIVE_BER, nvlink5: true},
	fieldNVLinkSymbolBER:         {id: gonvml.FI_DEV_NVLINK_COUNT_SYMBOL_BER, nvlink5: true},
	fieldNVLinkRecoverySucceeded: {id: gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_SUCCESSFUL_EVENTS, nvlink5: true},
	fieldNVLinkRecoveryFailed:    {id: gonvml.FI_DEV_NVLINK_COUNT_LINK_RECOVERY_FAILED_EVENTS, nvlink5: true},
}

// nvlinkErrorCounters are code 71's counters, the ones DCGM's NVLink5 rule
// reads, with the names its messages use. Any growth is Fatal.
var nvlinkErrorCounters = []struct {
	field int
	name  string
}{
	{fieldNVLinkSymbolErrors, "symbol errors"},
	{fieldNVLinkMalformedPackets, "malformed packets"},
	{fieldNVLinkBufferOverruns, "packets dropped on buffer overrun"},
	{fieldNVLinkReceiveErrors, "receive errors"},
	{fieldNVLinkRemoteErrors, "remote receive errors"},
	{fieldNVLinkGeneralErrors, "general receive errors"},
	{fieldNVLinkIntegrityErrors, "local link integrity errors"},
	{fieldNVLinkRecoveryEvents, "link recovery events"},
	{fieldNVLinkEffectiveErrors, "effective errors"},
	{fieldNVLinkECCErrors, "ECC data errors"},
}

// gpuHealth is one poll's values for one GPU.
type gpuHealth struct {
	lost            bool
	fields          [numHealthFields]reading
	powerUnreadable reading // 1 when the power read failed, observed unless the GPU was lost
	unrepairable    reading
	computeMajor    reading
	inforom         reading // 1 when corrupted; only read when a validation is due
	mig             bool    // MIG mode is on; a read that fails counts as off, as in DCGM
	linksDown       []int   // links that read disabled; not read on a GPU in MIG mode
}

// Poll reads every GPU once and returns this poll's health conditions. ok is
// false when the poll read nothing: NVML is not initialized, the GPU count
// could not be read, or an earlier poll is still stuck in the driver. Call it
// every HealthInterval from one goroutine.
func (h *Health) Poll() (conds []monitor.Condition, ok bool) {
	lib, syms, ready := h.reader.Library()
	if !ready {
		return nil, false
	}
	now := h.now()
	validate := h.inforomAt.IsZero() || now.Sub(h.inforomAt) >= inforomInterval
	type polled struct {
		gpus []gpuHealth
		ok   bool
	}
	result, status := run(h.check, func() polled {
		count, ret := lib.DeviceGetCount()
		if outcome("nvmlDeviceGetCount", ret) != Observed {
			return polled{}
		}
		gpus := make([]gpuHealth, count)
		for i := range gpus {
			gpus[i] = readGPUHealth(lib, i, syms.unrepairableMemoryFlag, validate)
		}
		return polled{gpus, true}
	})
	if status == Stuck || !result.ok {
		// The readings are dropped and the baselines kept, so a counter that
		// grew meanwhile is reported by the next poll that answers.
		return nil, false
	}
	if validate {
		h.inforomAt = now
		h.inforom = make([]reading, len(result.gpus))
		for i, g := range result.gpus {
			h.inforom[i] = g.inforom
		}
	}
	return h.evaluate(now, result.gpus, h.reader.gpuIdentities()), true
}

// readGPUHealth reads one GPU. It runs on the check's goroutine and only
// makes NVML calls.
func readGPUHealth(lib gonvml.Interface, index int, hasUnrepairable, validateInforom bool) gpuHealth {
	var r gpuReader
	dev, ret := lib.DeviceGetHandleByIndex(index)
	if !r.observe("nvmlDeviceGetHandleByIndex", 0, ret).ok {
		return gpuHealth{lost: r.lost}
	}

	// One step per read, in a fixed order.
	var g gpuHealth
	steps := []func(){
		// The CUDA compute capability's major version: code 132 applies
		// from Ampere, and Blackwell changes how the NVLink5 fields are
		// read and code 121's floor.
		func() {
			major, _, ret := dev.GetCudaComputeCapability()
			g.computeMajor = r.observe("nvmlDeviceGetCudaComputeCapability", uint64(major), ret)
		},
		// Codes 4, 6, 7, 8, 10, 12, 14's link count, 71, 80, 119, 121,
		// 125-128 and 132: every field value in one call. Each value carries
		// its own return code.
		func() {
			blackwell := g.computeMajor.ok && g.computeMajor.value >= blackwellComputeMajor
			values := make([]gonvml.FieldValue, 0, numHealthFields)
			index := make([]int, 0, numHealthFields)
			for i, f := range healthFields {
				if f.nvlink5 && !g.computeMajor.ok {
					// Without the architecture the scope is unknown, and
					// a counter read at another scope than last poll's
					// would look like it jumped.
					continue
				}
				v := gonvml.FieldValue{FieldId: f.id}
				if f.nvlink5 && blackwell {
					v.ScopeId = math.MaxUint32
				}
				values = append(values, v)
				index = append(index, i)
			}
			if !r.observe("nvmlDeviceGetFieldValues", 0, dev.GetFieldValues(values)).ok {
				return
			}
			for i, v := range values {
				n, isUint := fieldValueUint(v)
				if got := r.observe("nvmlDeviceGetFieldValues", n, gonvml.Return(v.NvmlReturn)); got.ok && isUint {
					g.fields[index[i]] = got
				}
			}
		},
		// Code 11: DCGM warns when board power can't be read, except on a
		// GPU that doesn't report power.
		func() {
			_, ret := dev.GetPowerUsage()
			switch outcome("nvmlDeviceGetPowerUsage", ret) {
			case Observed, NotObserved:
				g.powerUnreadable = reading{value: 0, ok: true}
			case GPULost:
				r.lost = true
			default:
				g.powerUnreadable = reading{value: 1, ok: true}
			}
		},
		// Code 52: the unrepairable-memory flag, on drivers that have it.
		func() {
			if hasUnrepairable {
				status, ret := dev.GetUnrepairableMemoryFlag_v1()
				g.unrepairable = r.observe("nvmlDeviceGetUnrepairableMemoryFlag_v1", uint64(status.BUnrepairableMemory), ret)
			}
		},
		// Code 14, first half: MIG mode. Only the current mode counts; a
		// pending mode needs a GPU reset to take effect.
		func() {
			current, _, ret := dev.GetMigMode()
			mode := r.observe("nvmlDeviceGetMigMode", uint64(current), ret)
			g.mig = mode.ok && mode.value == gonvml.DEVICE_MIG_ENABLE
		},
		// Code 14, second half: the state of each link the GPU reports,
		// skipped in MIG mode. A link whose state can't be read is skipped,
		// as in DCGM.
		func() {
			count := g.fields[fieldNVLinkCount]
			if g.mig || !count.ok {
				return
			}
			// NVML reports at most NVLINK_MAX_LINKS links, and DCGM's loop
			// stops there too.
			links := int(min(count.value, gonvml.NVLINK_MAX_LINKS))
			for link := range links {
				state, ret := dev.GetNvLinkState(link)
				if r.observe("nvmlDeviceGetNvLinkState", uint64(state), ret).ok && state == gonvml.FEATURE_DISABLED {
					g.linksDown = append(g.linksDown, link)
				}
				if r.lost {
					return
				}
			}
		},
		// Code 9: InfoROM validation, when one is due. Corrupted is a
		// reading, not an error.
		func() {
			if !validateInforom {
				return
			}
			switch ret := dev.ValidateInforom(); ret {
			case gonvml.ERROR_CORRUPTED_INFOROM:
				g.inforom = reading{value: 1, ok: true}
			default:
				g.inforom = r.observe("nvmlDeviceValidateInforom", 0, ret)
			}
		},
	}
	// A lost GPU answers every call with ERROR_GPU_IS_LOST, so stop at the
	// first one.
	for _, step := range steps {
		step()
		if r.lost {
			return gpuHealth{lost: true}
		}
	}
	return g
}

// evaluate applies the health rules to one poll's readings and updates the
// rate counters' baselines. A value that wasn't read is zero and not
// observed.
func (h *Health) evaluate(now time.Time, samples []gpuHealth, gpus []gpuIdentity) []monitor.Condition {
	// NVML reports the links of a GPU in MIG mode as down, and DCGM counts
	// no link as down while any GPU on the node is in MIG mode.
	anyMIG := false
	for _, s := range samples {
		anyMIG = anyMIG || s.mig
	}

	var out []monitor.Condition
	for i, s := range samples {
		if s.lost {
			// A lost GPU's values are not observed, so its counters need two
			// new readings before they can report growth.
			h.clearBaselines(i)
			out = append(out, lostGPU(i, gpus)...)
			continue
		}
		gpu := describeGPU(i, gpus)
		fatal := func(code int, detail string) {
			out = append(out, healthCondition(code, monitor.SeverityFatal, gpu, detail))
		}
		warn := func(code int, observed, over bool, detail string) {
			if h.notified.report(now, levelKey{i, strconv.Itoa(code)}, observed, over) {
				out = append(out, healthCondition(code, monitor.SeverityWarning, gpu, detail))
			}
		}
		growth := func(field int) (uint64, bool) {
			return h.grew(healthCounterKey{i, field}, s.fields[field])
		}
		f := s.fields
		blackwell := s.computeMajor.ok && s.computeMajor.value >= blackwellComputeMajor

		if dbe := f[fieldDBEVolatile]; dbe.ok && dbe.value > 0 {
			fatal(codeVolatileDBE, fmt.Sprintf("%d uncorrected volatile ECC errors", dbe.value))
		}

		pending := f[fieldRetiredPending]
		warn(codePendingRetirements, pending.ok, pending.value != 0, "pages are pending retirement")

		// A count that wasn't read adds 0, as in DCGM.
		sbe, dbe := f[fieldRetiredSBE], f[fieldRetiredDBE]
		switch total := sbe.value + dbe.value; {
		case total >= maxRetiredPagesTotal:
			fatal(codeRetiredPagesLimit, fmt.Sprintf("%d pages retired (%d single-bit, %d double-bit), at or above the limit of %d",
				total, sbe.value, dbe.value, maxRetiredPagesTotal))
		case dbe.ok && dbe.value > maxRetiredPagesDBE:
			fatal(codeRetiredPagesDBELimit, fmt.Sprintf("%d pages retired after double-bit errors, above the limit of %d",
				dbe.value, maxRetiredPagesDBE))
		}

		// Slowdown times are in nanoseconds.
		thermal, thermalObserved := growth(fieldThermalViolation)
		warn(codeClocksThermal, thermalObserved, thermal > 0,
			fmt.Sprintf("clocks were slowed by a thermal limit for %v since the last check", time.Duration(thermal)))
		power, powerObserved := growth(fieldPowerViolation)
		warn(codeClocksPower, powerObserved, power > 0,
			fmt.Sprintf("clocks were slowed by a power limit for %v since the last check", time.Duration(power)))

		if !anyMIG {
			for _, link := range s.linksDown {
				fatal(codeNVLinkDown, fmt.Sprintf("NVLink %d is down", link))
			}
		}

		// Every counter updates its baseline, and each that grew reports,
		// as each is its own incident in DCGM.
		for _, c := range nvlinkErrorCounters {
			if n, _ := growth(c.field); n > 0 {
				fatal(codeNVLinkErrorCritical, fmt.Sprintf("NVLink %s grew by %d since the last check", c.name, n))
			}
		}
		// DCGM reads these lifetime counts only on Blackwell and newer,
		// where they replace the lifetime error counts of older GPUs.
		if blackwell {
			if failed := f[fieldNVLinkRecoveryFailed]; failed.ok && failed.value > 0 {
				fatal(codeNVLinkErrorCritical, fmt.Sprintf("failed NVLink recoveries: %d", failed.value))
			}
			succeeded := f[fieldNVLinkRecoverySucceeded]
			warn(codeNVLinkErrorCritical, succeeded.ok, succeeded.value > 0,
				fmt.Sprintf("successful NVLink recoveries: %d", succeeded.value))
		}

		if ber := f[fieldNVLinkEffectiveBER]; ber.ok {
			if m, e := berParts(ber.value); effectiveBERExceeded(m, e) {
				fatal(codeEffectiveBER, fmt.Sprintf("NVLink effective BER %de-%d, above 1e-%d", m, e, effectiveBERExponent))
			}
		}
		if ber := f[fieldNVLinkSymbolBER]; ber.ok {
			floor := uint64(symbolBERFloorExponent)
			if blackwell {
				floor = symbolBERFloorExponentSummed
			}
			if m, e := berParts(ber.value); symbolBERExceeded(m, e, floor) {
				fatal(codeSymbolBER, fmt.Sprintf("NVLink symbol BER %de-%d, at or above 1e-%d", m, e, floor-1))
			}
		}

		if failed := f[fieldRemapFailure]; failed.ok && failed.value > 0 {
			fatal(codeRowRemapFailure, "row remapping failed")
		}

		unc := f[fieldRemapUncorrectable]
		warn(codeUncorrectableRemaps, unc.ok && s.computeMajor.ok,
			s.computeMajor.value >= ampereComputeMajor && unc.value >= maxUncorrectableRemaps,
			fmt.Sprintf("%d uncorrectable row remaps, at or above the limit of %d", unc.value, maxUncorrectableRemaps))

		if s.unrepairable.ok && s.unrepairable.value != 0 {
			fatal(codeFaultyMemory, "the GPU reports unrepairable memory")
		}

		// From the last validation, which runs at most hourly.
		inforom := h.inforom[i]
		warn(codeCorruptInforom, inforom.ok, inforom.value != 0, "the InfoROM is corrupted")

		warn(codePowerUnreadable, s.powerUnreadable.ok, s.powerUnreadable.value != 0, "power usage could not be read")

		action := f[fieldRecoveryAction]
		if action.ok {
			switch gonvml.DeviceGpuRecoveryAction(action.value) {
			case gonvml.GPU_RECOVERY_ACTION_GPU_RESET:
				fatal(codeRecoveryReset, "NVML recovery action: reset the GPU")
			case gonvml.GPU_RECOVERY_ACTION_NODE_REBOOT:
				fatal(codeRecoveryReboot, "NVML recovery action: reboot the node")
			case gonvml.GPU_RECOVERY_ACTION_DRAIN_AND_RESET:
				fatal(codeRecoveryDrainReset, "NVML recovery action: drain peer-to-peer traffic and reset the GPU")
			}
		}
		warn(codeRecoveryDrainP2P, action.ok, action.value == uint64(gonvml.GPU_RECOVERY_ACTION_DRAIN_P2P),
			"NVML recovery action: drain peer-to-peer traffic")
	}
	return out
}

// grew records r as the counter's baseline and returns how much it grew since
// the previous poll. observed is false when r wasn't read or there is no
// previous reading. A failed read clears the baseline. A drop is a counter
// reset: observed, with no growth. Unlike the policy counters there is no
// MaxUint32 bound, because slowdown times are nanoseconds and pass it in
// seconds.
func (h *Health) grew(key healthCounterKey, r reading) (n uint64, observed bool) {
	prev, had := h.baselines[key]
	if !r.ok {
		delete(h.baselines, key)
		return 0, false
	}
	h.baselines[key] = r.value
	if !had {
		return 0, false
	}
	if r.value < prev {
		return 0, true
	}
	return r.value - prev, true
}

func (h *Health) clearBaselines(gpu int) {
	for key := range h.baselines {
		if key.gpu == gpu {
			delete(h.baselines, key)
		}
	}
}

// berParts unpacks an NVLink BER field value into its mantissa and exponent:
// BER = mantissa x 10^-exponent.
func berParts(v uint64) (mantissa, exponent uint64) {
	return (v >> gonvml.NVLINK_BER_MANTISSA_SHIFT) & gonvml.NVLINK_BER_MANTISSA_WIDTH,
		(v >> gonvml.NVLINK_BER_EXP_SHIFT) & gonvml.NVLINK_BER_EXP_WIDTH
}

// effectiveBERExceeded reports whether an effective BER is above 1e-12, as
// DCGM's IsEffectiveBerThresholdExceeded does. 10e-13 equals 1e-12.
func effectiveBERExceeded(mantissa, exponent uint64) bool {
	if mantissa == berZeroMantissa && exponent == berZeroExponent {
		return false
	}
	switch exponent {
	case effectiveBERExponent:
		return mantissa > 1 // above 1e-12
	case effectiveBERExponent + 1:
		return mantissa > 10 // above 10e-13
	}
	return exponent < effectiveBERExponent
}

// symbolBERExceeded reports whether a symbol BER's exponent is below the
// floor, as DCGM's IsSymbolBerThresholdExceeded does. A zero mantissa is a
// BER of zero. DCGM also fails a mantissa above 15 at the floor, which the
// 4-bit mantissa can't hold.
func symbolBERExceeded(mantissa, exponent, floor uint64) bool {
	return mantissa != 0 && exponent < floor
}

// healthCondition builds DCGMHealthCode<code>, the reason the DCGM reader
// reports for the same verdict, with the GPU named in the message.
func healthCondition(code int, severity monitor.Severity, gpu, detail string) monitor.Condition {
	return reasons.DCGMHealthCode.Builder(code).Message(gpu + ": " + detail).Severity(severity).Build()
}
