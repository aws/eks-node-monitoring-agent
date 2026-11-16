// Package nitrosandbox monitors the runtime that runs each isolated pod inside
// its own microVM. Its CRI proxy sits between kubelet and containerd and serves
// the endpoint kubelet is configured with, so when the proxy stops serving, the
// node loses every pod, not just the isolated ones.
//
// Kubelet notices that faster than any poll here can, but it cannot say what
// broke: with the endpoint gone kubelet exits before posting status, so the node
// object shows only "Kubelet stopped posting node status" from the node
// lifecycle controller, and the socket path appears solely in kubelet's journal.
// This monitor puts the cause on the node object.
//
// It is dormant on nodes that do not run isolated pods: the plugin is not even
// registered there, so nothing names it at any log level.
package nitrosandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/go-logr/logr"
	cri "k8s.io/cri-api/pkg/apis"
	criclient "k8s.io/cri-client/pkg"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
	"github.com/aws/eks-node-monitoring-agent/pkg/util"
)

const (
	// criProxyUnit serves kubelet's CRI endpoint on nodes running isolated pods.
	// It is Restart=always with RestartSec=5, so a crash loop keeps reporting
	// ActiveState=active between restarts and only the restart count gives it
	// away.
	criProxyUnit = "cri-proxy.service"

	// criProxySocket is the endpoint kubelet dials. Losing it costs the node
	// every pod.
	criProxySocket = "/run/nitro-sandbox/cri-proxy.sock"

	// enclaveDevice and pcieSlots exist only on hosts that can run sandboxes.
	enclaveDevice = "/dev/nitro_enclaves"
	pcieSlots     = "/sys/class/misc/nitro_enclaves/pcie_slots"

	// checkInterval matches the cadence of NMA's other polling monitors. It is
	// also what keeps routine churn out of the condition: a containerd restart
	// takes the proxy down with it (Requires=) and the proxy needs about a
	// second to rebind, which a five-minute poll will almost never sample.
	checkInterval = 5 * time.Minute

	// unhealthyPollsBeforeReporting is the number of consecutive bad polls
	// required before reporting. A Fatal condition never returns to True while
	// the agent runs, so a single unlucky sample must not condemn a node that
	// recovered on its own.
	unhealthyPollsBeforeReporting = 2

	// socketDialTimeout bounds the connect to the proxy socket, so a wedged
	// proxy cannot stall the poll loop.
	socketDialTimeout = 2 * time.Second

	// criTimeout bounds calls to the container runtime, so a wedged runtime
	// cannot stall the poll loop either.
	criTimeout = 5 * time.Second

	// execConditionResult is systemd's Result for a unit whose ExecCondition
	// returned non-zero, i.e. one deliberately skipped. On nodes where sandbox
	// support is switched off, the proxy unit is skipped rather than started,
	// and that is not a fault.
	execConditionResult = "exec-condition"
)

// activationTags are the runtime context tags whose presence activates this
// monitor. Pod isolation ships on EKS Auto first; when the runtime is enabled
// for every node type this becomes an empty list, meaning any node, and nothing
// else has to change.
var activationTags = []string{config.EKSAuto}

var _ monitor.Monitor = (*nitroSandboxMonitor)(nil)

// unitStatus is the subset of systemd unit state this monitor reads.
type unitStatus struct {
	// activeState is systemd's ActiveState, e.g. "active" or "failed".
	activeState string
	// result is systemd's Result, e.g. "success" or "exec-condition".
	result string
	// restarts is systemd's NRestarts, which only grows while a unit runs.
	restarts uint32
}

type nitroSandboxMonitor struct {
	manager monitor.Manager
	log     logr.Logger

	// runtimeService is the CRI client, created on first use.
	runtimeService cri.RuntimeService

	// restartsSeen is NRestarts as of the previous poll, so restarts that
	// happen while the agent watches can be told apart from ones that predate
	// it, such as after an agent restart on a long-lived node.
	restartsSeen *uint32
	// unhealthyPolls counts consecutive bad polls, and resets on a good one.
	unhealthyPolls int
	// leakedPolls counts consecutive polls that found the slots exhausted.
	leakedPolls int

	// Injection points for tests; a node's real state is not reproducible in a
	// unit test.
	tags          func() []string
	unitStatus    func(context.Context, string) (unitStatus, error)
	dialSocket    func(string) error
	fileExists    func(string) bool
	diskSandboxes func() ([]diskSandbox, error)
	criSandboxIDs func(context.Context) ([]string, error)
	slotBudget    func() (int, error)
}

func NewNitroSandboxMonitor() *nitroSandboxMonitor {
	m := &nitroSandboxMonitor{
		tags:          func() []string { return config.GetRuntimeContext().Tags() },
		unitStatus:    systemdUnitStatus,
		dialSocket:    dialUnixSocket,
		fileExists:    hostFileExists,
		diskSandboxes: hostDiskSandboxes,
		slotBudget:    hostSlotBudget,
	}
	m.criSandboxIDs = m.listCRISandboxIDs
	return m
}

func hostFileExists(path string) bool {
	_, err := os.Stat(config.ToHostPath(path))
	return err == nil
}

// hardwareSupportsSandboxes reports whether this host can run isolated pods.
// Both paths are created by the enclave driver, so their absence means no
// sandbox will ever start here. It only reads the filesystem, so it is safe to
// call from init before the runtime context exists.
func hardwareSupportsSandboxes() bool {
	return hostFileExists(enclaveDevice) && hostFileExists(pcieSlots)
}

func (m *nitroSandboxMonitor) Name() string {
	return "nitro-sandbox"
}

func (m *nitroSandboxMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *nitroSandboxMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	m.log = log.FromContext(ctx).WithName("nitro-sandbox")

	// A node's type cannot change under a running agent, so this is evaluated
	// once. Sandbox support is re-checked on every poll instead, because a node
	// can gain it without the agent restarting.
	if !m.activatedByTags() {
		return nil
	}

	for _, check := range []func(context.Context) error{
		m.checkCRIProxy,
		m.checkLeakedSandboxes,
	} {
		handler := util.NewChannelHandler(
			func(time.Time) error { return check(ctx) },
			util.TimeTickWithJitterContext(ctx, checkInterval),
		)
		go handler.Start(ctx)
	}

	return nil
}

// activatedByTags reports whether this node's type is one the monitor runs on.
// An empty activationTags means every node type.
func (m *nitroSandboxMonitor) activatedByTags() bool {
	if len(activationTags) == 0 {
		return true
	}
	tags := m.tags()
	return slices.ContainsFunc(activationTags, func(tag string) bool {
		return slices.Contains(tags, tag)
	})
}

// sandboxSupported reports whether this node can run isolated pods at all. Both
// paths are created by the enclave driver, so their absence means no sandbox
// will ever start here and there is nothing to report.
func (m *nitroSandboxMonitor) sandboxSupported() bool {
	return m.fileExists(enclaveDevice) && m.fileExists(pcieSlots)
}

// checkCRIProxy reports CRIProxyUnhealthy when the CRI proxy is not serving the
// endpoint kubelet depends on.
func (m *nitroSandboxMonitor) checkCRIProxy(ctx context.Context) error {
	if !m.sandboxSupported() {
		return nil
	}

	status, err := m.unitStatus(ctx, criProxyUnit)
	if err != nil {
		// An unreadable unit state is not evidence of a fault, and reporting on
		// it would turn a dbus problem into a node replacement.
		return fmt.Errorf("reading %s state, %w", criProxyUnit, err)
	}

	// A unit skipped by its own ExecCondition is not a fault: sandbox support is
	// switched off on this node even though the hardware is present.
	if status.result == execConditionResult {
		m.unhealthyPolls = 0
		return nil
	}

	restarted := m.restartsSeen != nil && status.restarts > *m.restartsSeen
	m.restartsSeen = &status.restarts

	fault := ""
	switch {
	case status.activeState == "failed":
		fault = fmt.Sprintf("unit failed (%s)", status.result)
	case status.activeState != "active":
		fault = fmt.Sprintf("unit %s", status.activeState)
	case restarted:
		// Restart=always hides crashes behind an active unit, so a growing
		// restart count is the only evidence that it is not staying up.
		fault = fmt.Sprintf("unit restarted (%d total)", status.restarts)
	default:
		if err := m.dialSocket(config.ToHostPath(criProxySocket)); err != nil {
			fault = "socket not accepting connections"
		}
	}

	if fault == "" {
		m.unhealthyPolls = 0
		return nil
	}

	m.unhealthyPolls++
	if m.unhealthyPolls < unhealthyPollsBeforeReporting {
		m.log.V(2).Info("cri-proxy looks unhealthy, waiting for confirmation",
			"fault", fault, "polls", m.unhealthyPolls)
		return nil
	}

	return m.manager.Notify(ctx, reasons.CRIProxyUnhealthy.Builder().
		Message(fmt.Sprintf("cri-proxy is not serving kubelet's CRI endpoint %s: %s", criProxySocket, fault)).
		Build())
}

// systemdUnitStatus reads unit state over dbus. A connection is opened per call
// to match the runtime monitor: polls are minutes apart, and holding one open
// across a systemd restart is its own failure mode.
func systemdUnitStatus(ctx context.Context, unit string) (unitStatus, error) {
	dc, err := dbus.NewWithContext(ctx)
	if err != nil {
		return unitStatus{}, fmt.Errorf("establishing dbus connection, %w", err)
	}
	defer dc.Close()

	properties, err := dc.GetAllPropertiesContext(ctx, unit)
	if err != nil {
		return unitStatus{}, fmt.Errorf("getting properties for unit %q, %w", unit, err)
	}

	status := unitStatus{}
	if activeState, ok := properties["ActiveState"].(string); ok {
		status.activeState = activeState
	}
	if result, ok := properties["Result"].(string); ok {
		status.result = result
	}
	if restarts, ok := properties["NRestarts"].(uint32); ok {
		status.restarts = restarts
	}
	return status, nil
}

// listCRISandboxIDs returns the ids of every sandbox the container runtime
// knows about. The client is created on first use and reused: it targets
// containerd directly rather than the CRI proxy, so the list stays readable
// while the proxy itself is broken.
func (m *nitroSandboxMonitor) listCRISandboxIDs(ctx context.Context) ([]string, error) {
	if m.runtimeService == nil {
		service, err := criclient.NewRemoteRuntimeService(ctx, config.CRIEndpoint, criTimeout, nil, false)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s, %w", config.CRIEndpoint, err)
		}
		m.runtimeService = service
	}

	sandboxes, err := m.runtimeService.ListPodSandbox(ctx, nil)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(sandboxes))
	for _, sandbox := range sandboxes {
		ids = append(ids, sandbox.Id)
	}
	return ids, nil
}

func dialUnixSocket(path string) error {
	conn, err := net.DialTimeout("unix", path, socketDialTimeout)
	if err != nil {
		return err
	}
	return conn.Close()
}
