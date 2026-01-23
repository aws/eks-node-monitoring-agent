package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/osext"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ monitor.Monitor = (*runtimeMonitor)(nil)

func NewRuntimeMonitor(node *corev1.Node, kubeClient client.Client) *runtimeMonitor {
	return &runtimeMonitor{
		node:             node,
		kubeClient:       kubeClient,
		unitRestartCount: map[string]uint32{},
	}
}

const (
	containerdDeprecationInterval = time.Hour
)

type runtimeMonitor struct {
	node             *corev1.Node
	kubeClient       client.Client
	unitRestartCount map[string]uint32

	manager monitor.Manager
}

func (m *runtimeMonitor) Name() string {
	return "container-runtime"
}

func (m *runtimeMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *runtimeMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	rtCtx := config.GetRuntimeContext()
	for _, subscriber := range []util.SubscriptionArgs[string]{
		{
			Handler: m.handleKubelet,
			SubscriptionFn: func() (<-chan string, error) {
				return mgr.Subscribe(resource.ResourceTypeJournal, []resource.Part{"kubelet"})
			},
		},
		{
			Handler: m.handleSystemd,
			SubscriptionFn: func() (<-chan string, error) {
				return mgr.Subscribe(resource.ResourceTypeJournal, []resource.Part{"systemd"})
			},
		},
	} {
		handler, err := util.NewChannelHandlerFromSubscriptionArgs(m.manager, subscriber)
		if err != nil {
			return err
		}
		go handler.Start(ctx)
	}

	containerdHandler := util.NewChannelHandler(func(time.Time) error { return m.handleContainerd() }, util.TimeTickWithJitterContext(ctx, containerdDeprecationInterval))
	go containerdHandler.Start(ctx)

	if !slices.Contains(rtCtx.Tags(), config.Bottlerocket) {
		handler := util.NewChannelHandler(func(time.Time) error { return m.handleSystemdServices() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute))
		go handler.Start(ctx)
	}

	return nil
}

var failedToStartRegexp = regexp.MustCompile(`Failed to start (.*?)\.`)

// handleSystemdServices look at the NRestarts property for all systemd units and reports
// those which are restarting
func (m *runtimeMonitor) handleSystemdServices() error {
	ctx := context.Background()
	dc, err := dbus.NewWithContext(ctx)
	if err != nil {
		return fmt.Errorf("establishing dbus connection, %w", err)
	}
	defer dc.Close()
	units, err := dc.ListUnitsContext(ctx)
	if err != nil {
		return fmt.Errorf("listing units, %w", err)
	}
	for _, unit := range units {
		// we only look at services
		if !strings.HasSuffix(unit.Name, ".service") {
			continue
		}

		properties, err := dc.GetAllPropertiesContext(ctx, unit.Name)
		if err != nil {
			return fmt.Errorf("getting properties for unit %q, %w", unit.Name, err)
		}
		nRestarts, ok := properties["NRestarts"]
		if !ok {
			continue
		}
		currentRestarts, ok := nRestarts.(uint32)
		if !ok {
			return fmt.Errorf("unexpected type for NRestarts, %T", nRestarts)
		}
		if currentRestarts == 0 {
			// just saves storing the current restart count for services that never restart
			continue
		}

		previousRestarts := m.unitRestartCount[unit.Name]
		m.unitRestartCount[unit.Name] = currentRestarts
		const MinOccurrences = 3
		if currentRestarts > MinOccurrences && currentRestarts > previousRestarts {
			// we don't use the MinOccurrences support in the manager as we know exactly
			// how many times it was restarted already and it would only increase once per
			// five minutes if a service is constantly restarting. By doing the check ourself
			// we can always report more accurately.
			return m.manager.Notify(context.Background(),
				reasons.RepeatedRestart.
					Builder(unitName(unit.Name)).
					Message(fmt.Sprintf("Systemd unit %q has restarted (NRestarts %d -> %d)", unit.Name, previousRestarts, currentRestarts)).
					Build(),
			)
		}
	}
	return nil
}

func unitName(s string) string {
	s = strings.TrimSuffix(s, ".service")
	s = cases.Title(language.English).String(s)
	return s
}

func (m *runtimeMonitor) handleSystemd(line string) error {
	if match := failedToStartRegexp.FindStringSubmatch(line); match != nil {
		return m.manager.Notify(context.Background(),
			reasons.ServiceFailedToStart.
				Builder().
				Message(line).
				Build(),
		)
	}
	return nil
}

var (
	enteredFailedState     = regexp.MustCompile(`Unit kubelet.service entered failed state`)
	ociRuntimeCreateFailed = regexp.MustCompile(`OCI runtime create failed: (.*?)`)
	podStuckTerminating    = regexp.MustCompile(`"Pod still has one or more containers in the non-exited state and will not be removed from desired state" pod="(.*)"`)
)
var (
	readinessProbeRegexp  = regexp.MustCompile(`Readiness probe for ".*?:(.*)" failed`)
	readinessProbeLimiter = rate.NewLimiter(rate.Every(time.Second), 5)
	livenessProbeRegexp   = regexp.MustCompile(`Liveness probe for ".*?:(.*)" failed`)
	livenessProbeLimiter  = rate.NewLimiter(rate.Every(time.Second), 5)
)

func (m *runtimeMonitor) handleKubelet(line string) error {
	if matches := podStuckTerminating.FindStringSubmatch(line); matches != nil {
		podName := matches[1]
		return m.manager.Notify(context.TODO(),
			reasons.PodStuckTerminating.
				Builder().
				Message(fmt.Sprintf("Pod %q is stuck terminating. Restarting kubelet will result in cleaning up stuck pods", podName)).
				// TODO: gauge this occurence count, or put this in a separate
				// handler with a longer cadence to avoid reporting issues early
				MinOccurrences(2).
				Build(),
		)
	} else if enteredFailedState.MatchString(line) {
		return m.manager.Notify(context.TODO(),
			reasons.KubeletFailed.
				Builder().
				Message("Kubelet has entered a failed state").
				Build(),
		)
	} else if ociRuntimeCreateFailed.MatchString(line) {
		return m.manager.Notify(context.TODO(),
			reasons.ContainerRuntimeFailed.
				Builder().
				Message("OCI runtime create failed").
				Build())
	} else if readinessProbeRegexp.MatchString(line) && !readinessProbeLimiter.Allow() {
		return m.manager.Notify(context.TODO(),
			reasons.ReadinessProbeFailures.
				Builder().
				Message("Exceeded a safe rate of readiness probe failures").
				Build(),
		)
	} else if livenessProbeRegexp.MatchString(line) && !livenessProbeLimiter.Allow() {
		return m.manager.Notify(context.TODO(),
			reasons.LivenessProbeFailures.
				Builder().
				Message("Exceeded a safe rate of liveness probe failures").
				Build(),
		)
	}
	return nil
}

func (m *runtimeMonitor) handleContainerd() (merr error) {
	out, err := osext.NewExec(config.HostRoot()).Command("ctr", "deprecations", "list", "--format=json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list containerd deprecations: %w", err)
	}
	return m.checkContainerdWarnings(out)
}

type deprecationWarning struct {
	ID             string    `json:"id"`
	LastOccurrence time.Time `json:"lastOccurrence"`
	Message        string    `json:"message"`
}

func (m *runtimeMonitor) checkContainerdWarnings(data []byte) (merr error) {
	if len(data) == 0 {
		if err := m.reconcileManifestWarning(context.TODO(), []deprecationWarning{}); err != nil {
			return err
		}
		return nil
	}
	var deprecationWarnings []deprecationWarning
	if err := json.Unmarshal(data, &deprecationWarnings); err != nil {
		return fmt.Errorf("could not parse containerd deprecation warnings: %w", err)
	}
	// we only want to keep the deprecations that have occurred since the last
	// internal, because even after the issue is resolved the deprecation will
	// be present from the api with the old timestamp.
	var filteredDeprecationWarnings []deprecationWarning
	for _, deprecationWarning := range deprecationWarnings {
		if time.Since(deprecationWarning.LastOccurrence) < containerdDeprecationInterval {
			filteredDeprecationWarnings = append(filteredDeprecationWarnings, deprecationWarning)
		}
	}
	for _, warning := range filteredDeprecationWarnings {
		merr = errors.Join(merr, m.manager.Notify(context.TODO(),
			reasons.DeprecatedContainerdConfiguration.
				Builder().
				Message(fmt.Sprintf("%s: %s", warning.ID, warning.Message)).
				Build(),
		))
	}
	merr = errors.Join(merr, m.reconcileManifestWarning(context.TODO(), filteredDeprecationWarnings))
	return merr
}

const (
	dockerManifestV2SchemaV1Key = "manifest-v2-schema-v1-detection"
	eksDomain                   = "aws.amazoneks.com"
)

var dockerManifestV2SchemaV1Annotation = path.Join(eksDomain, dockerManifestV2SchemaV1Key)

func (m *runtimeMonitor) reconcileManifestWarning(ctx context.Context, deprecations []deprecationWarning) error {
	// TODO: consider reorganizing so that this get excluded at build time.
	if !slices.Contains(config.GetRuntimeContext().Tags(), config.EKSAuto) {
		return nil
	}

	var firstDeprecation *deprecationWarning
	// find the first manifest-v2-schema-v1 deprecation in the list
	// NOTE: this currently assumes the container deprecations are sorted
	for _, deprecation := range deprecations {
		if deprecation.ID == "io.containerd.deprecation/pull-schema-1-image" {
			firstDeprecation = &deprecation
			break
		}
	}

	if err := m.kubeClient.Get(ctx, client.ObjectKeyFromObject(m.node), m.node); err != nil {
		return err
	}
	nodeStored := m.node.DeepCopy()

	if m.node.Annotations == nil {
		m.node.Annotations = map[string]string{}
	}

	if firstDeprecation != nil {
		m.node.Annotations[dockerManifestV2SchemaV1Annotation] = firstDeprecation.LastOccurrence.Format(time.RFC3339Nano)
	} else if lastOccurrence, exists := m.node.Annotations[dockerManifestV2SchemaV1Annotation]; exists {
		timestamp, err := time.Parse(time.RFC3339Nano, lastOccurrence)
		// if we fail to parse the timestamp the safest move is to remove the annotation
		// if we have not detected anything new within 1 hour then remove the annotation
		if err != nil || time.Since(timestamp) > containerdDeprecationInterval {
			delete(m.node.Annotations, dockerManifestV2SchemaV1Annotation)
		}
	} else {
		// there are no warnings and no existing annotation, so nothing to update
		return nil
	}

	return m.kubeClient.Patch(ctx, m.node, client.MergeFrom(nodeStored))
}
