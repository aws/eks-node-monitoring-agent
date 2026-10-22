package nvml

import (
	"fmt"
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

// Health limits, the values DCGM uses (common/DcgmGPUHardwareLimits.h).
const (
	maxRetiredPagesTotal   = 63  // single-bit plus double-bit retired pages
	maxRetiredPagesDBE     = 15  // pages retired after double-bit errors
	maxUncorrectableRemaps = 512 // uncorrectable row remaps, Ampere and newer
	ampereComputeMajor     = 8   // first CUDA compute capability with row remapping
)

// DCGM health codes (dcgm_errors.h) that Health reports.
const (
	codeVolatileDBE          = 4
	codePendingRetirements   = 6
	codeRetiredPagesLimit    = 7
	codeRetiredPagesDBELimit = 8
	codeCorruptInforom       = 9
	codePowerUnreadable      = 11
	codeFaultyMemory         = 52
	codeRowRemapFailure      = 80
	codeRecoveryReset        = 125
	codeRecoveryReboot       = 126
	codeRecoveryDrainP2P     = 127
	codeRecoveryDrainReset   = 128
	codeUncorrectableRemaps  = 132
)

// Health reports the DCGM health-watch codes that read a GPU's current state,
// as DCGMHealthCode<N> with DCGM's verdict: FAIL is Fatal, WARN a Warning.
// Each rule reads the value DCGM reads for it (DcgmHealthWatch.cpp, DCGM
// 4.6.1):
//
//	Code  Rule                                                NVML source                          Severity
//	4     uncorrected volatile ECC errors > 0, any location   field ECC_DBE_VOL_TOTAL              Fatal
//	6     pages pending retirement                            field RETIRED_PENDING                Warning
//	7     single-bit plus double-bit retired pages >= 63      fields RETIRED_SBE, RETIRED_DBE      Fatal
//	8     double-bit retired pages > 15, unless 7 fired       field RETIRED_DBE                    Fatal
//	9     InfoROM corrupted, checked at most hourly           nvmlDeviceValidateInforom            Warning
//	11    power can't be read                                 nvmlDeviceGetPowerUsage              Warning
//	52    unrepairable memory                                 nvmlDeviceGetUnrepairableMemoryFlag  Fatal
//	80    row remapping failed                                field REMAPPED_FAILURE               Fatal
//	125   recovery action: reset the GPU                      field GET_GPU_RECOVERY_ACTION        Fatal
//	126   recovery action: reboot the node                    field GET_GPU_RECOVERY_ACTION        Fatal
//	127   recovery action: drain peer-to-peer traffic         field GET_GPU_RECOVERY_ACTION        Warning
//	128   recovery action: drain and reset the GPU            field GET_GPU_RECOVERY_ACTION        Fatal
//	132   uncorrectable row remaps >= 512, Ampere and newer   field REMAPPED_UNC                   Warning
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
// rate (3), NVLink4 error counters (13, 70, and 71's counter paths), fabric
// fields (33, 123), and the XID-derived codes. IMEX (122) stays skipped.
//
// One poll reads every GPU as a single run of one check under DefaultTimeout.
type Health struct {
	reader *Reader
	check  *Check
	now    func() time.Time

	// Only the goroutine that calls Poll touches the fields below.
	// inforomAt is when the InfoROMs were last validated, and inforom holds
	// each GPU's result until the next validation.
	inforomAt time.Time
	inforom   []reading
	notified  levelReports
}

// NewHealth registers the health check with r. It reads every GPU, so it
// counts toward the stall. Create it before the first call to r.Reconcile.
func NewHealth(r *Reader) *Health {
	return &Health{
		reader:   r,
		check:    r.NewCheck("health", DefaultTimeout),
		now:      time.Now,
		notified: levelReports{},
	}
}

// Indexes into healthFieldIDs and gpuHealth.fields.
const (
	fieldDBEVolatile = iota
	fieldRetiredPending
	fieldRetiredSBE
	fieldRetiredDBE
	fieldRemapFailure
	fieldRemapUncorrectable
	fieldRecoveryAction
	numHealthFields
)

// healthFieldIDs are the NVML field values Health reads, in one
// nvmlDeviceGetFieldValues call per GPU.
var healthFieldIDs = [numHealthFields]uint32{
	fieldDBEVolatile:        gonvml.FI_DEV_ECC_DBE_VOL_TOTAL,
	fieldRetiredPending:     gonvml.FI_DEV_RETIRED_PENDING,
	fieldRetiredSBE:         gonvml.FI_DEV_RETIRED_SBE,
	fieldRetiredDBE:         gonvml.FI_DEV_RETIRED_DBE,
	fieldRemapFailure:       gonvml.FI_DEV_REMAPPED_FAILURE,
	fieldRemapUncorrectable: gonvml.FI_DEV_REMAPPED_UNC,
	fieldRecoveryAction:     gonvml.FI_DEV_GET_GPU_RECOVERY_ACTION,
}

// gpuHealth is one poll's values for one GPU.
type gpuHealth struct {
	lost            bool
	fields          [numHealthFields]reading
	powerUnreadable reading // 1 when the power read failed, observed unless the GPU was lost
	unrepairable    reading
	computeMajor    reading
	inforom         reading // 1 when corrupted; only read when a validation is due
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
		// Codes 4, 6, 7, 8, 80, 132 and 125-128: every field value in one
		// call. Each value carries its own return code.
		func() {
			values := make([]gonvml.FieldValue, numHealthFields)
			for i, id := range healthFieldIDs {
				values[i].FieldId = id
			}
			if !r.observe("nvmlDeviceGetFieldValues", 0, dev.GetFieldValues(values)).ok {
				return
			}
			for i, v := range values {
				n, isUint := fieldValueUint(v)
				if got := r.observe("nvmlDeviceGetFieldValues", n, gonvml.Return(v.NvmlReturn)); got.ok && isUint {
					g.fields[i] = got
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
		// Code 132 applies from Ampere: the CUDA compute capability's major
		// version.
		func() {
			major, _, ret := dev.GetCudaComputeCapability()
			g.computeMajor = r.observe("nvmlDeviceGetCudaComputeCapability", uint64(major), ret)
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

// evaluate applies the health rules to one poll's readings. A value that
// wasn't read is zero and not observed.
func (h *Health) evaluate(now time.Time, samples []gpuHealth, gpus []gpuIdentity) []monitor.Condition {
	var out []monitor.Condition
	for i, s := range samples {
		if s.lost {
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
		f := s.fields

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

// healthCondition builds DCGMHealthCode<code>, the reason the DCGM reader
// reports for the same verdict, with the GPU named in the message.
func healthCondition(code int, severity monitor.Severity, gpu, detail string) monitor.Condition {
	return reasons.DCGMHealthCode.Builder(code).Message(gpu + ": " + detail).Severity(severity).Build()
}
