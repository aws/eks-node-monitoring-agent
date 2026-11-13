package nvml

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/internal/pkg/instanceinfo"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/devicefiles"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

// DevicesInterval is how often Devices.Poll should run. It matches the
// interval of the DCGM reader's fabric field and device count checks.
const DevicesInterval = 5 * time.Minute

// fabricStartWindow is how long a GPU's fabric can stay untrained on an
// instance whose Fabric Manager runs on the host before it is reported. It
// leaves room for a slow Fabric Manager start after boot.
const fabricStartWindow = 15 * time.Minute

// Devices reads the GPU count and each GPU's fabric state, and reports GPUs
// that have fallen off the bus. It reports:
//   - The GPU count against the host's /dev/nvidia<N> files and against the
//     count the EC2 instance type provides.
//   - Each GPU's Fabric Manager status and fabric health mask, translated
//     the way DCGM translates them. A driver without the fabric call reads as
//     NvmlTooOld, a Warning rate-limited like the DCGM reader's.
//   - A GPU whose fabric has stayed untrained, reading NotStarted or
//     InProgress on every poll for fabricStartWindow, on an instance type
//     whose Fabric Manager runs on the host, as FabricManagerNotStarted. The
//     driver reads NotStarted until the GPU sends its first fabric probe and
//     InProgress until Fabric Manager answers, so a Fabric Manager that never
//     starts usually shows as InProgress. On other instance types NotStarted
//     can mean no Fabric Manager is needed, so nothing is reported, and
//     nothing is on an instance type that can't be looked up. Any other
//     state, or a read that isn't observed, starts the window over.
//   - One lost-GPU Fatal for each GPU that answered with ERROR_GPU_IS_LOST.
//     That GPU's fabric is not read, and the counts are not compared in the
//     same poll, so one lost GPU yields one condition.
//
// One poll reads every GPU as a single run of one check under
// DefaultTimeout, so a hung driver holds at most one thread for all of them.
type Devices struct {
	reader       *Reader
	check        *Check
	instanceInfo instanceinfo.InstanceTypeInfoProvider

	// deviceFiles counts the host's GPU device files. fabricV3 and fabricV2
	// read one GPU's fabric info with each struct version. go-nvml's fabric
	// call returns a handler that calls the C library directly, which a mock
	// Device can't fake, so tests replace these.
	deviceFiles func() (uint, string, error)
	fabricV3    func(gonvml.Device) (gonvml.GpuFabricInfo_v3, gonvml.Return)
	fabricV2    func(gonvml.Device) (gonvml.GpuFabricInfo_v2, gonvml.Return)

	now func() time.Time

	// Only the goroutine that calls Poll touches the fields below.
	// untrainedSince holds, for each GPU whose fabric was untrained on the
	// last poll, the poll at which it became so.
	fabricWarnings *renotify.Tracker[string]
	untrainedSince map[int]time.Time
}

// NewDevices registers the device checks with r. info gives the GPU count
// for the instance type; nil skips that comparison. Create it before the
// first call to r.Reconcile.
func NewDevices(r *Reader, info instanceinfo.InstanceTypeInfoProvider) *Devices {
	return &Devices{
		reader:       r,
		check:        r.NewCheck("devices", DefaultTimeout),
		instanceInfo: info,
		deviceFiles:  devicefiles.Count,
		fabricV3: func(dev gonvml.Device) (gonvml.GpuFabricInfo_v3, gonvml.Return) {
			return dev.GetGpuFabricInfoV().V3()
		},
		fabricV2: func(dev gonvml.Device) (gonvml.GpuFabricInfo_v2, gonvml.Return) {
			return dev.GetGpuFabricInfoV().V2()
		},
		now:            time.Now,
		fabricWarnings: renotify.New[string](classify.WarningReNotifyInterval),
		untrainedSince: map[int]time.Time{},
	}
}

// Poll reads every GPU once and returns this poll's device conditions. ok is
// false when the poll read nothing: NVML is not initialized, the GPU count
// could not be read, or an earlier poll is still stuck in the driver. Call it
// every DevicesInterval from one goroutine.
func (d *Devices) Poll(ctx context.Context) (conds []monitor.Condition, ok bool) {
	lib, syms, ready := d.reader.Library()
	if !ready {
		return nil, false
	}
	result, status := run(d.check, func() devicesReading {
		return d.read(lib, syms.gpuFabricInfoV)
	})
	if status == Stuck || !result.ok {
		return nil, false
	}
	// NvmlTooOld and an unrecognized fabric state report on every poll for
	// as long as the node runs this driver, so they are rate-limited.
	return classify.SuppressRepeatedWarnings(d.fabricWarnings, d.now(), d.evaluate(ctx, result, d.reader.gpuIdentities()), func(c monitor.Condition) bool {
		return c.Reason == reasons.FabricManagerNotRunningWarning.Template()
	}), true
}

// devicesReading is one poll's NVML values. ok is false when the GPU count
// could not be read.
type devicesReading struct {
	ok    bool
	count int
	gpus  []gpuDevice
}

// gpuDevice is one GPU's fabric reading. fmStatus is in the
// dcgmFabricManagerStatus_t encoding Classify reads. fmStatus and healthMask
// are nil when not observed.
type gpuDevice struct {
	lost       bool
	fmStatus   *int64
	healthMask *uint64
}

// read runs on the check's goroutine and only makes NVML calls; Poll
// evaluates the readings.
func (d *Devices) read(lib gonvml.Interface, hasFabricInfo bool) devicesReading {
	count, ret := lib.DeviceGetCount()
	if outcome("nvmlDeviceGetCount", ret) != Observed {
		return devicesReading{}
	}
	out := devicesReading{ok: true, count: count, gpus: make([]gpuDevice, count)}
	for i := range out.gpus {
		out.gpus[i] = d.readGPU(lib, i, hasFabricInfo)
	}
	return out
}

func (d *Devices) readGPU(lib gonvml.Interface, index int, hasFabricInfo bool) gpuDevice {
	dev, ret := lib.DeviceGetHandleByIndex(index)
	switch outcome("nvmlDeviceGetHandleByIndex", ret) {
	case Observed:
	case GPULost:
		return gpuDevice{lost: true}
	default:
		return gpuDevice{}
	}

	// A driver without the fabric call reads as NvmlTooOld, as in DCGM.
	tooOld := classify.DcgmFMStatusNvmlTooOld
	if !hasFabricInfo {
		return gpuDevice{fmStatus: &tooOld}
	}
	state, fmStatus, healthMask, ret := d.readFabric(dev)
	switch outcome("nvmlDeviceGetGpuFabricInfoV", ret) {
	case Observed:
		status := fabricManagerStatus(state, fmStatus)
		mask := uint64(healthMask)
		return gpuDevice{fmStatus: &status, healthMask: &mask}
	case GPULost:
		return gpuDevice{lost: true}
	default:
		// FunctionMissing cannot happen here: readFabric is reached only
		// when the symbol is present, and a present symbol never reports
		// itself missing. A missing symbol is handled above by hasFabricInfo.
		return gpuDevice{}
	}
}

// readFabric reads one GPU's fabric info. NVML's nvmlDeviceGetGpuFabricInfoV
// takes a versioned struct; it tries the newer nvmlGpuFabricInfo_v3 layout
// first and falls back to the older _v2 when the driver rejects v3
// (ERROR_ARGUMENT_VERSION_MISMATCH), the same negotiation DCGM does. readGPU
// calls this only when the fabric symbol is present, so the call cannot
// report the function as missing.
func (d *Devices) readFabric(dev gonvml.Device) (state uint8, status, healthMask uint32, ret gonvml.Return) {
	v3, ret := d.fabricV3(dev)
	if ret != gonvml.ERROR_ARGUMENT_VERSION_MISMATCH {
		return v3.State, v3.Status, v3.HealthMask, ret
	}
	v2, ret := d.fabricV2(dev)
	return v2.State, v2.Status, v2.HealthMask, ret
}

// fabricManagerStatus translates NVML's fabric state and status into the
// dcgmFabricManagerStatus_t value that DCGM stores for the same reading.
func fabricManagerStatus(state uint8, status uint32) int64 {
	switch state {
	case gonvml.GPU_FABRIC_STATE_NOT_SUPPORTED:
		return classify.DcgmFMStatusNotSupported
	case gonvml.GPU_FABRIC_STATE_NOT_STARTED:
		return classify.DcgmFMStatusNotStarted
	case gonvml.GPU_FABRIC_STATE_IN_PROGRESS:
		return classify.DcgmFMStatusInProgress
	case gonvml.GPU_FABRIC_STATE_COMPLETED:
		// Training finished; status is its nvmlReturn_t result.
		if gonvml.Return(status) == gonvml.SUCCESS {
			return classify.DcgmFMStatusSuccess
		}
		return classify.DcgmFMStatusFailure
	default:
		return classify.DcgmFMStatusUnrecognized
	}
}

// evaluate classifies one poll's readings.
func (d *Devices) evaluate(ctx context.Context, r devicesReading, gpus []gpuIdentity) []monitor.Condition {
	info := d.instanceType(ctx)
	now := d.now()
	var out []monitor.Condition
	anyLost := false
	for i, g := range r.gpus {
		if g.lost {
			anyLost = true
			delete(d.untrainedSince, i)
			out = append(out, lostGPU(i, gpus)...)
			continue
		}
		out = append(out, classify.Classify(classify.NormalizedSignals{
			FabricManagerStatus: g.fmStatus,
			FabricHealthMask:    g.healthMask,
		})...)
		if d.fabricUntrained(now, i, g) && info != nil && info.HostFabricManager {
			out = append(out, reasons.FabricManagerNotStarted.Builder().
				Message(fmt.Sprintf("%s: the NVLink fabric is not trained after at least %v, Fabric Manager status: %s",
					describeGPU(i, gpus), fabricStartWindow, untrainedStatusName(*g.fmStatus))).
				Build())
		}
	}
	// A lost GPU can also make the counts disagree, and it is already
	// reported, so the counts are compared only when no GPU is lost.
	if anyLost {
		return out
	}
	return append(out, d.countConditions(ctx, uint(r.count), info, gpus)...)
}

// fabricUntrained records this poll's fabric reading for GPU i and reports
// whether its fabric has been untrained, NotStarted or InProgress, on every
// poll for at least fabricStartWindow. A change between the two keeps the
// window.
func (d *Devices) fabricUntrained(now time.Time, i int, g gpuDevice) bool {
	if g.fmStatus == nil || (*g.fmStatus != classify.DcgmFMStatusNotStarted && *g.fmStatus != classify.DcgmFMStatusInProgress) {
		delete(d.untrainedSince, i)
		return false
	}
	since, ok := d.untrainedSince[i]
	if !ok {
		d.untrainedSince[i] = now
		return false
	}
	return now.Sub(since) >= fabricStartWindow
}

// untrainedStatusName names an untrained fabric state for the message.
func untrainedStatusName(status int64) string {
	if status == classify.DcgmFMStatusInProgress {
		return "InProgress"
	}
	return "NotStarted"
}

// instanceType looks up this instance type's hardware. It returns nil when
// there is no lookup, or the instance type can't be read or isn't in it.
func (d *Devices) instanceType(ctx context.Context) *instanceinfo.InstanceInfo {
	if d.instanceInfo == nil {
		return nil
	}
	logger := log.FromContext(ctx)
	info, err := d.instanceInfo.GetInstanceInfo(ctx)
	switch {
	case errors.Is(err, instanceinfo.ErrUnknownInstanceType):
		logger.V(4).Info("instance type not in embedded lookup, skipping checks that need it", "error", err)
		return nil
	case err != nil:
		logger.V(2).Info("could not determine the instance type, skipping checks that need it", "error", err)
		return nil
	}
	return info
}

// countConditions compares the GPU count against the host's device files and
// against the instance type's GPU count. A comparison whose other side can't
// be read is skipped; info is nil when the instance type isn't known.
func (d *Devices) countConditions(ctx context.Context, count uint, info *instanceinfo.InstanceInfo, gpus []gpuIdentity) []monitor.Condition {
	logger := log.FromContext(ctx)
	signals := classify.NormalizedSignals{GPUCount: &count}

	files, dir, err := d.deviceFiles()
	if err != nil {
		logger.Error(err, "could not count NVIDIA device files", "dir", dir)
	} else {
		signals.GPUDeviceFileCount, signals.GPUDeviceFilePath = &files, dir
	}

	if info != nil {
		signals.ExpectedGPUCount = &info.NvidiaGPUCount
	}

	conds := classify.Classify(signals)
	// A GPU missing from NVML's count never enumerated, so NVML has no UUID
	// for it. Listing the GPUs that are present still ties the condition to
	// this set of GPUs.
	if present := presentGPUs(gpus); present != "" {
		for i := range conds {
			conds[i].Message += "; GPUs present: " + present
		}
	}
	return conds
}

// presentGPUs lists the UUIDs Init recorded, or "" when it recorded none.
func presentGPUs(gpus []gpuIdentity) string {
	var uuids []string
	for _, g := range gpus {
		if g.uuid != "" {
			uuids = append(uuids, g.uuid)
		}
	}
	return strings.Join(uuids, ", ")
}

// lostGPU reports a GPU that answered an NVML call with ERROR_GPU_IS_LOST.
// NVIDIA documents XID 79 as a GPU that has fallen off the bus, so it uses
// the reason a real XID 79 event reports, and the two land on one condition
// reason.
func lostGPU(index int, gpus []gpuIdentity) []monitor.Condition {
	return withMessage(classify.NormalizedSignals{XIDs: []uint{79}},
		fmt.Sprintf("%s has fallen off the bus: NVML returned GPU_IS_LOST", describeGPU(index, gpus)))
}

// describeGPU names the GPU at index, with the UUID and serial Init recorded
// for it. Indexes can change between boots; the UUID and serial don't.
func describeGPU(index int, gpus []gpuIdentity) string {
	name := fmt.Sprintf("GPU %d", index)
	if index >= len(gpus) {
		// Init couldn't read the GPU list.
		return name
	}
	g := gpus[index]
	var ids []string
	if g.uuid != "" {
		ids = append(ids, "UUID "+g.uuid)
	}
	if g.serial != "" {
		ids = append(ids, "serial "+g.serial)
	}
	if len(ids) == 0 {
		return name
	}
	return name + " (" + strings.Join(ids, ", ") + ")"
}
