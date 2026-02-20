// IGNORE TEST COVERAGE: this component communicates with an nv-hostengine
// running in a separate location, and cannot be unit tested.

//go:build !darwin

package dcgm

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type DCGM interface {
	// Reconcile mends the connection to the DCGM host and initializes any of
	// the required components for live-detection of issues, including policy
	// violations and health check systems.
	Reconcile(context.Context) (bool, error)

	// PolicyViolationChannel returns a stream of DCGM notifications for
	// predefined healthy detection policies.
	// see: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html#policy
	PolicyViolationChannel() <-chan dcgmapi.PolicyViolation

	// RunDiag runs active DCGM health check diagnostics and returns the result.
	// see: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html#active-health-checks
	// see: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/dcgm-diagnostics.html
	RunDiag(dcgmapi.DiagType) (dcgmapi.DiagResults, error)

	// HealthCheck fetches passive DCGM health check information. the health
	// check systems have to be first enabled.
	// see: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html#background-health-checks
	HealthCheck() (dcgmapi.HealthResponse, error)

	// GetValuesSince retrieves all field value changes since the last call.
	// see: https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html#
	GetValuesSince(time.Time) ([]dcgmapi.FieldValue_v2, error)

	// GetDeviceCount returns the number of GPUs visible via NVML.
	GetDeviceCount() (uint, error)
}

var ErrNotInitialized = fmt.Errorf("DCGM is not initialized")

var _ DCGM = &dcgmHelper{}

type DCGMConfig struct {
	// The host address of the running nv-hostengine instance. accepted in
	// 'host:port' pair.
	Address string

	InitializationGracePeriod time.Duration

	// Features is a list of options to control which DCGM features to initialize.
	Features []Feature
}

type Feature string

const (
	FeatureHealthSystems     Feature = "HealthSystems"
	FeaturePolicyViolations  Feature = "PolicyViolations"
	FeatureActiveDiagnostics Feature = "ActiveDiagnostics"
	FeatureFields            Feature = "Fields"
)

// NewDCGM returns a dcgm wrapper that implement the DCGM interface.
//
// NOTE: this implementation requires libdcgmapi.so.4
func NewDCGM(config DCGMConfig) *dcgmHelper {
	if config.Address == "" {
		if address, ok := os.LookupEnv("DCGM_ADDRESS"); ok {
			config.Address = address
		} else {
			config.Address = "localhost:5555"
		}
	}
	return &dcgmHelper{
		initialized:         false,
		lastShutdown:        time.Now(),
		policyViolationChan: make(chan dcgmapi.PolicyViolation),
		config:              config,
	}
}

type dcgmHelper struct {
	config           DCGMConfig
	initialized      bool
	lastShutdown     time.Time
	shutdownHandlers []func()
	// a proxy channel to send real policy violation events into once the DCGM
	// module has been initialized/reinitialized. this prevents edge cases
	// inherited from returning the channel itself.
	policyViolationChan chan dcgmapi.PolicyViolation

	fieldHandle dcgmapi.FieldHandle
}

func (d *dcgmHelper) PolicyViolationChannel() <-chan dcgmapi.PolicyViolation {
	return d.policyViolationChan
}

func (d *dcgmHelper) RunDiag(diagType dcgmapi.DiagType) (dcgmapi.DiagResults, error) {
	if !slices.Contains(d.config.Features, FeatureActiveDiagnostics) {
		return dcgmapi.DiagResults{}, nil
	}
	if !d.initialized {
		return dcgmapi.DiagResults{}, ErrNotInitialized
	}
	return dcgmapi.RunDiag(diagType, dcgmapi.GroupAllGPUs())
}

func (d *dcgmHelper) Reconcile(ctx context.Context) (bool, error) {
	logger := log.FromContext(ctx)
	if d.initialized {
		// simple health check by getting DCGM properties
		_, err := dcgmapi.Introspect()
		if err == nil {
			return false, nil
		}
		logger.Info("failed to health check DCGM", "introspection error", err.Error())
	}
	if err := d.shutdown(); err != nil {
		return false, fmt.Errorf("failed to shutdown DCGM: %w", err)
	}
	// Initializes a DCGM client in Standalone mode, which connects to an
	// already running nv-hostengine. The process needs to be running somewhere
	// on the node reachable via hostNetworking.
	if _, err := dcgmapi.Init(dcgmapi.Standalone, d.config.Address, "0"); err != nil {
		// avoid returning a 'failed to initialize' error if we just
		// disconnected from the DCGM host.
		if time.Now().Before(d.lastShutdown.Add(d.config.InitializationGracePeriod)) {
			return false, nil
		}
		return false, fmt.Errorf("failed to initialize DCGM: %w", err)
	}
	d.initialized = true

	if slices.Contains(d.config.Features, FeaturePolicyViolations) {
		// DCGM defines lets clients define error policies and notifies subscribers
		// whenever there's a violation. These policies cover most of the important
		// GPU error detection cases.
		policyCtx, cancelPolicyViolationChannel := context.WithCancel(ctx)
		policyChan, err := dcgmapi.ListenForPolicyViolations(policyCtx,
			dcgmapi.DbePolicy,     // Fires if there's a double bit error
			dcgmapi.XidPolicy,     // Fires on XID errors. This should overlap with other policy violations
			dcgmapi.NvlinkPolicy,  // Fires if NVLink errors detected by DCGM
			dcgmapi.MaxRtPgPolicy, // Fires if the number of page retirements has hit the limit RT page faults
			dcgmapi.PowerPolicy,   // Fires on violations for power limits
			dcgmapi.ThermalPolicy, // Fires on abnormal thermal status of GPUs
			dcgmapi.PCIePolicy,    // Fires on pcie replays for transmission errors
		)
		// SAFETY: actual cleanup of the goroutine in dcgmapi.ListenForPolicyViolations
		// only happens when the context is finished, even if the host engine
		// crashes. the policy registrations all utilize the same channel (its a go
		// static), so not cleaning up the routine could result in events being
		// sniped and/or leaked.
		if err != nil {
			cancelPolicyViolationChannel()
			return false, fmt.Errorf("failed to register DCGM policies: %w", err)
		}
		d.shutdownHandlers = append(d.shutdownHandlers, cancelPolicyViolationChannel)
		// forward the policy events until the source channel is closed.
		go func() {
			for p := range policyChan {
				d.policyViolationChan <- p
			}
		}()
	}

	if slices.Contains(d.config.Features, FeatureHealthSystems) {
		// DCGM requires us to configure which health systems to actively monitor in
		// order to get results when calling `dcgmapi.HealthCheck`.
		if err := dcgmapi.HealthSet(dcgmapi.GroupAllGPUs(), dcgmapi.DCGM_HEALTH_WATCH_ALL); err != nil {
			return false, fmt.Errorf("failed to enable DCGM health check system watchers: %w", err)
		}
	}

	if slices.Contains(d.config.Features, FeatureFields) {
		if code := dcgmapi.FieldsInit(); code != 0 {
			return false, fmt.Errorf("failed to initialize DCGM fields modules")
		}
		fieldHandle, err := dcgmapi.FieldGroupCreate("watch", []dcgmapi.Short{
			// used to check reasons that the clock cycle is delayed due to
			// hardware conditions.
			dcgmapi.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			// SXID errors caused by the NVSwitch.
			dcgmapi.DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS,
			dcgmapi.DCGM_FI_DEV_NVSWITCH_NON_FATAL_ERRORS,
		})
		if err != nil {
			return false, err
		}
		err = dcgmapi.WatchFieldsWithGroup(fieldHandle, dcgmapi.GroupAllGPUs())
		if err != nil {
			return false, err
		}
		d.fieldHandle = fieldHandle
		d.shutdownHandlers = append(d.shutdownHandlers, func() {
			dcgmapi.FieldGroupDestroy(fieldHandle)
			dcgmapi.FieldsTerm()
		})
	}

	return true, nil
}

func (d *dcgmHelper) HealthCheck() (dcgmapi.HealthResponse, error) {
	if !slices.Contains(d.config.Features, FeatureHealthSystems) {
		return dcgmapi.HealthResponse{}, nil
	}
	if !d.initialized {
		return dcgmapi.HealthResponse{}, ErrNotInitialized
	}
	// NOTE: the first time the HealthCheck is called will be a noop, but
	// following calls with return the new errors since the last call.
	healthRes, err := dcgmapi.HealthCheck(dcgmapi.GroupAllGPUs())
	if err != nil {
		return dcgmapi.HealthResponse{}, fmt.Errorf("failed to call DCGM health check: %w", err)
	}
	return healthRes, nil
}

func (d *dcgmHelper) GetValuesSince(since time.Time) ([]dcgmapi.FieldValue_v2, error) {
	if !slices.Contains(d.config.Features, FeatureFields) {
		return []dcgmapi.FieldValue_v2{}, nil
	}
	if !d.initialized {
		return []dcgmapi.FieldValue_v2{}, ErrNotInitialized
	}
	values, _, err := dcgmapi.GetValuesSince(dcgmapi.GroupAllGPUs(), d.fieldHandle, since)
	return values, err
}

func (d *dcgmHelper) GetDeviceCount() (uint, error) {
	if !d.initialized {
		return 0, ErrNotInitialized
	}
	return dcgmapi.GetAllDeviceCount()
}

func (d *dcgmHelper) shutdown() error {
	if d.initialized {
		defer func() {
			d.shutdownHandlers = nil
			d.initialized = false
			d.lastShutdown = time.Now()
		}()
		for _, shutdownHandler := range d.shutdownHandlers {
			shutdownHandler()
		}
		return dcgmapi.Shutdown()
	}
	return nil
}
