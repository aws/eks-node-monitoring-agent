package networking

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	cri "github.com/containerd/containerd/integration/cri-api/pkg/apis"
	"github.com/containerd/containerd/integration/remote"
	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/go-logr/logr"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/time/rate"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/efa"
	toolexec "golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/exec"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/ipamd"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/iptables"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/networkutils"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/osext"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
)

var _ monitor.Monitor = (*NetworkingMonitor)(nil)

const (
	criIPCacheTTL = 2 * time.Hour

	// TODO: should we shorten this? it'll take 2 monitor periods for terminal issues to be raised
	interfaceMonitorPeriod = 5 * time.Minute
	// we only need to store the interfaces from our previous monitor period
	interfaceCacheTTL = interfaceMonitorPeriod * 2
)

type criIPDetails struct {
	ip          string
	containerId string
	name        string
	namespace   string
}

type NetworkingMonitor struct {
	manager            monitor.Manager
	ctrdRuntimeService cri.RuntimeService
	criIPCache         cache.Store // containerID -> IP and metadata
	interfaceCache     cache.Store
	log                logr.Logger
	exec               osext.Exec
	runtimeContext     *config.RuntimeContext
}

func (m *NetworkingMonitor) Name() string {
	return "networking"
}

func (m *NetworkingMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func criCacheFunc(obj interface{}) (string, error) {
	return obj.(criIPDetails).containerId, nil
}

func interfaceCacheKeyFunc(obj any) (string, error) {
	return obj.(net.Interface).Name, nil
}

type Option func(*NetworkingMonitor)

func WithExec(exec osext.Exec) Option {
	return func(m *NetworkingMonitor) {
		m.exec = exec
	}
}

func WithRuntimeContext(runtimeContext *config.RuntimeContext) Option {
	return func(m *NetworkingMonitor) {
		m.runtimeContext = runtimeContext
	}
}

func NewNetworkingMonitor(options ...Option) *NetworkingMonitor {
	m := &NetworkingMonitor{
		exec:           osext.NewExec(config.HostRoot()),
		runtimeContext: config.GetRuntimeContext(),
	}

	for _, option := range options {
		option(m)
	}

	return m
}

func (m *NetworkingMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	m.manager = mgr
	m.log = log.FromContext(ctx)
	m.criIPCache = cache.NewTTLStore(criCacheFunc, criIPCacheTTL)
	go func() {
		// this TTL cache is lazy and uses on-access expiration. so we
		// periodically touch all of the items to prevent containers that havent
		// been seen in some time from occupying memory.
		for range util.TimeTickWithJitterContext(ctx, criIPCacheTTL) {
			m.criIPCache.List()
		}
	}()

	m.interfaceCache = cache.NewTTLStore(interfaceCacheKeyFunc, interfaceCacheTTL)
	go func() {
		// this TTL cache is lazy and uses on-access expiration. so we
		// periodically touch all of the items to prevent interfaces that havent
		// been seen in some time from occupying memory.
		for range util.TimeTickWithJitterContext(ctx, interfaceCacheTTL) {
			m.interfaceCache.List()
		}
	}()

	subscriptionArgs := []util.SubscriptionArgs[string]{
		{
			Handler: m.handleIPAMDLogs,
			SubscriptionFn: func() (<-chan string, error) {
				return mgr.Subscribe(resource.ResourceTypeFile, []resource.Part{resource.Part(config.IPAMDLogPath)})
			},
		},
	}

	// Walk the kubernetes pod logs directory to find the log stream for
	// kube-proxy. TODO: This is brittle as it can change when the pod restarts,
	// so we need to trigger this search periodically or find a better
	// source/heuristic for getting pod logs.
	filepath.WalkDir(config.PodLogsDirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(path, "kube-proxy") {
			subscriptionArgs = append(subscriptionArgs, util.SubscriptionArgs[string]{
				Handler: m.handleKubeProxy,
				SubscriptionFn: func() (<-chan string, error) {
					return mgr.Subscribe(resource.ResourceTypeFile, []resource.Part{resource.Part(path)})
				},
			})
		}
		return nil
	})

	for _, subArgs := range subscriptionArgs {
		handler, err := util.NewChannelHandlerFromSubscriptionArgs(m.manager, subArgs)
		if err != nil {
			return err
		}
		go handler.Start(ctx)
	}

	for _, handler := range []interface{ Start(context.Context) error }{
		util.NewChannelHandler(func(time.Time) error { return makeEthtoolMonitor(mgr).handleEthtool() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleIPRulesAndRoutes() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleIPTables() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleInterfaces() }, util.TimeTickWithJitterContext(ctx, interfaceMonitorPeriod)),
		util.NewChannelHandler(func(time.Time) error { return m.handleNetworkSysctl() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleIPAMD() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
		util.NewChannelHandler(func(time.Time) error { return m.handleMACAddressPolicy() }, util.TimeTickWithJitterContext(ctx, 5*time.Minute)),
	} {
		go handler.Start(ctx)
	}

	// EFA hardware counter monitoring
	efaSystem := efa.NewEFASystem()
	go func() {
		for range util.TimeTickWithJitterContext(ctx, time.Minute) {
			conditions, err := efaSystem.HardwareCounters(ctx)
			if err != nil {
				m.log.Error(err, "failed to check EFA hardware counters")
				continue
			}
			for _, condition := range conditions {
				if err := m.manager.Notify(ctx, condition); err != nil {
					m.log.Error(err, "failed to notify EFA condition")
				}
			}
		}
	}()

	return nil
}

var failedToWatchListRegex = regexp.MustCompile(`Failed to watch (.*): failed to list (.*): the server could not find the requested resource`)

func (m *NetworkingMonitor) handleKubeProxy(line string) error {
	if match := failedToWatchListRegex.FindStringSubmatch(line); match != nil {
		return m.manager.Notify(context.TODO(),
			reasons.KubeProxyNotReady.
				Builder().
				Message("Kube Proxy has failed to watch or list resources, which may impact pod to service communication.").
				Build(),
		)
	}
	return nil
}

// ~~~~ IPAMD ~~~~

func (m *NetworkingMonitor) handleIPAMDLogs(line string) error {
	if strings.Contains(line, "Failed to check API server connectivity: invalid configuration: no configuration has been provided") {
		return m.manager.Notify(context.TODO(),
			reasons.IPAMDNotReady.
				Builder().
				Message("IPAM-D Missing Service Account Token and the IPAM daemon cannot connect to the API server.").
				Build(),
		)
	} else if strings.Contains(line, "Unable to reach API Server") || strings.Contains(line, "Failed to check API server connectivity") {
		return m.manager.Notify(context.TODO(),
			reasons.IPAMDNotReady.
				Builder().
				Message("IPAM-D has failed to connect to API Server which could be an issue with IPTable rules or any other network configuration.").
				Build(),
		)
	} else if strings.Contains(line, "Starting L-IPAMD") {
		return m.manager.Notify(context.TODO(),
			reasons.IPAMDRepeatedlyRestart.
				Builder().
				Message("IPAMD is restarting often which leads to other CNI components failing.").
				MinOccurrences(5).
				Build(),
		)
	} else if strings.Contains(line, "InsufficientFreeAddressesInSubnet") {
		return m.manager.Notify(context.TODO(),
			reasons.IPAMDNoIPs.
				Builder().
				Message("There are no available IP addresses for IPAMD to assign to pods").
				Build(),
		)
	}

	return nil
}

func (m *NetworkingMonitor) handleIPAMD() error {
	if slices.Contains(m.runtimeContext.Tags(), config.EKSAuto) {
		// on the system, indicating ipamd should be running if there are no errors.
		dbus, err := dbus.NewWithContext(context.Background())
		if err != nil {
			return err
		}
		defer dbus.Close()
		property, err := dbus.GetUnitPropertyContext(context.Background(), "ipamd.service", "ActiveState")
		if err != nil {
			return err
		}
		ipamdRunning := false
		if property.Value.Value().(string) == "active" {
			ipamdRunning = true
		}
		return m.checkIPAMD(true /* EKS Auto comes with ipamd */, ipamdRunning)
	} else {
		ipamdShouldBeRunning := false
		ipamdRunning := false
		// Search pod logs to found out if the VPC CNI is presently running.
		dirs, err := os.ReadDir(config.PodLogsDirPath)
		if err != nil {
			return err
		}
		for _, path := range dirs {
			// aws-node pod implies that IPAMD will be started
			if strings.Contains(path.Name(), "aws-node") {
				ipamdShouldBeRunning = true
				// SAFETY: the current working slice is returned and will never be nil
				procs, _ := process.Processes()
				for _, proc := range procs {
					name, _ := proc.Name()
					if strings.Contains(name, "aws-k8s-agent") {
						ipamdRunning = true
					}
				}
				break
			}
		}
		return m.checkIPAMD(ipamdShouldBeRunning, ipamdRunning)
	}
}

func (m *NetworkingMonitor) checkIPAMD(ipamdShouldBeRunning bool, ipamdRunning bool) error {
	if ipamdShouldBeRunning && !ipamdRunning {
		return m.manager.Notify(context.Background(),
			reasons.IPAMDNotRunning.
				Builder().
				Message("The AWS VPC CNI was detected on this node but IPAMD was not found running").
				Build(),
		)
	}

	if ipamdShouldBeRunning && ipamdRunning {
		// To ensure that pods haven't been incorrectly assigned IPs, we discover all containers and their assigned IPs
		// as per the IPAMD checkpoint file, and cross-verify with the IPs assigned per the CRI.
		checkpointData, err := ipamd.GetCheckpoint()
		if err != nil {
			if os.IsNotExist(err) {
				// Skip this check if the file doesn't exist
				return nil
			}
			return err
		}
		if m.ctrdRuntimeService == nil {
			m.ctrdRuntimeService, err = remote.NewRuntimeService(config.CRIEndpoint, 5*time.Second)
			if err != nil {
				return err
			}
		}
		for _, entry := range checkpointData.Allocations {
			if _, ok, _ := m.criIPCache.GetByKey(entry.ContainerID); !ok {
				status, err := m.ctrdRuntimeService.PodSandboxStatus(entry.ContainerID)
				if err != nil {
					// If the CRI is down, or if the container doesn't exist in the CRI, skip this check.
					m.log.Info("failed to get pod sandbox status", "containerId", entry.ContainerID)
					continue
				} else {
					// cache responses from CRI, since each container will always be associated with the same IP for its lifetime
					m.criIPCache.Add(criIPDetails{
						ip:          status.Network.Ip,
						containerId: entry.ContainerID,
						name:        status.Metadata.Name,
						namespace:   status.Metadata.Namespace,
					})
				}
			}
			cacheEntry, exists, err := m.criIPCache.GetByKey(entry.ContainerID)
			if err != nil {
				return err
			}
			if !exists {
				m.log.Info("cache entry expired", "containerId", entry.ContainerID)
				continue
			}
			criDetails := cacheEntry.(criIPDetails)
			if !(criDetails.ip == entry.IPv4 || criDetails.ip == entry.IPv6) {
				return m.manager.Notify(context.Background(),
					reasons.IPAMDInconsistentState.
						Builder().
						Message(fmt.Sprintf("Internal IPAMD state has conflicting IP addresses for pod %s/%s", criDetails.namespace, criDetails.name)).
						Build(),
				)
			}
		}
	}
	return nil
}

// ~~~~ interfaces ~~~~

func (m *NetworkingMonitor) handleInterfaces() error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	return m.checkInterfaces(interfaces)
}

func (m *NetworkingMonitor) checkInterfaces(interfaces []net.Interface) error {
	hasLoopback := false
	for _, intf := range interfaces {
		// ignores things like docker0 for now.
		// NetworkingReady         False   Tue, 11 Mar 2025 14:47:30 +0000   Fri, 07 Mar 2025 21:27:30 +0000   InterfaceNotRunning          Interface "docker0" is not running
		if strings.HasPrefix(intf.Name, "docker") {
			continue
		}
		if net.FlagLoopback&intf.Flags != 0 {
			hasLoopback = true
		} else if net.FlagMulticast&intf.Flags == 0 || net.FlagBroadcast&intf.Flags == 0 {
			// Intended as a catch-all for dummy interfaces based on a pattern observed with
			// nodelocaldns and kube-proxy when using IPVS mode, but not a hard rule
			continue
		}

		obj, ok, _ := m.interfaceCache.GetByKey(intf.Name)
		defer m.interfaceCache.Add(intf)
		if !ok {
			continue
		}
		prevIntf := obj.(net.Interface)

		// only emit a fatal notification if the same issue is seen in adjacent monitor periods

		if interfaceHasConsistentIssue(net.FlagUp, prevIntf, intf) {
			return m.manager.Notify(context.TODO(),
				reasons.InterfaceNotUp.
					Builder().
					Message(fmt.Sprintf("Interface Name: %q, MAC: %q is not up", intf.Name, intf.HardwareAddr.String())).
					Build(),
			)
		}
		if interfaceHasConsistentIssue(net.FlagRunning, prevIntf, intf) {
			return m.manager.Notify(context.TODO(),
				reasons.InterfaceNotRunning.
					Builder().
					Message(fmt.Sprintf("Interface Name: %q, MAC: %q is not up", intf.Name, intf.HardwareAddr.String())).
					Build(),
			)
		}
	}
	if !hasLoopback {
		return m.manager.Notify(context.TODO(),
			reasons.MissingLoopbackInterface.
				Builder().
				Message("The loopback interface is missing from this instance. Without it, many services which depend on local connectivity will fail").
				Build(),
		)
	}
	return nil
}

func interfaceHasConsistentIssue(f net.Flags, pre net.Interface, cur net.Interface) bool {
	return cur.Flags&f == 0 && pre.Flags&f == 0
}

// ~~~~ ethtool ~~~~

// ENA driver metrics: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/monitoring-network-performance-ena.html#network-performance-metrics
const (
	BandwidthInExceeded  = "bw_in_allowance_exceeded"
	BandwidthOutExceeded = "bw_out_allowance_exceeded"
	ConntrackExceeded    = "conntrack_allowance_exceeded"
	LinkLocalExceeded    = "linklocal_allowance_exceeded"
	PPSExceeded          = "pps_allowance_exceeded"
)

type ethtoolMonitor struct {
	statExceededCache map[compoundStatKey]*statTracker
	manager           monitor.Manager
}

type statTracker struct {
	recorded    int
	rateLimiter *rate.Limiter
}

var statReasons = map[string]reasons.ReasonMeta{
	BandwidthInExceeded:  reasons.BandwidthInExceeded,
	BandwidthOutExceeded: reasons.BandwidthOutExceeded,
	ConntrackExceeded:    reasons.ConntrackExceeded,
	LinkLocalExceeded:    reasons.LinkLocalExceeded,
	PPSExceeded:          reasons.PPSExceeded,
}

var statLimiterConstructors = map[string]func() *rate.Limiter{
	BandwidthInExceeded:  func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Minute), 180) },
	BandwidthOutExceeded: func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Minute), 100) },
	ConntrackExceeded:    func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Minute), 100) },
	LinkLocalExceeded:    func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Minute), 100) },
	PPSExceeded:          func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Minute), 320) },
}

type compoundStatKey string

// maps are first keyed by interface name and then important NIC stat
func makeCompoundStatKey(interfaceName, statName string) compoundStatKey {
	return compoundStatKey(interfaceName + "-" + statName)
}

func makeEthtoolMonitor(manager monitor.Manager) *ethtoolMonitor {
	return &ethtoolMonitor{
		manager:           manager,
		statExceededCache: make(map[compoundStatKey]*statTracker),
	}
}

func (m *ethtoolMonitor) handleEthtool() (merr error) {
	netInterfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, netInterface := range netInterfaces {
		ethtoolCmd := []string{"ethtool", "-S", netInterface.Name}
		ethtoolOut, err := osext.NewExec(config.HostRoot()).Command(ethtoolCmd[0], ethtoolCmd[1:]...).CombinedOutput()
		if err != nil {
			// its ok for this to fail with exit code 94, which happens on an
			// interface with no stats like the loopback interface.
			if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 94 {
				merr = errors.Join(merr, fmt.Errorf("failed command %q: %w", ethtoolCmd, err))
				continue
			}
		}
		stats, err := parseEthtool(ethtoolOut)
		if err != nil {
			merr = errors.Join(merr, err)
			continue
		}
		merr = errors.Join(merr, m.checkEthtool(netInterface.Name, stats))
	}
	return merr
}

// checkEthtool checks whether the allowance exceeded metrics from ethtool are
// breached at an unhealthy rate.
func (m *ethtoolMonitor) checkEthtool(interfaceName string, stats map[string]int) (merr error) {
	for statKey, rateLimiterConstructor := range statLimiterConstructors {
		if statValue, ok := stats[statKey]; ok {
			cacheKey := makeCompoundStatKey(interfaceName, statKey)
			if statCache, ok := m.statExceededCache[cacheKey]; ok {
				// we want to detect when these metrics could be responsible for issues on the
				// node. reporting when they increase or are non-zero gets pretty noisy and
				// doesn't directly indicate any issues. instead, the approach here is to only
				// emit the event when there is a noticeable spike in the exceeded stats.
				if exceededCntDelta := statValue - statCache.recorded; !statCache.rateLimiter.AllowN(time.Now(), exceededCntDelta) {
					merr = errors.Join(m.manager.Notify(context.TODO(),
						statReasons[statKey].
							Builder().
							Message(fmt.Sprintf("%s increased on interface %q from %d to %d", statKey, interfaceName, statCache.recorded, statValue)).
							Severity(monitor.SeverityWarning).
							Build(),
					))
				}
				statCache.recorded = statValue
			} else {
				// if we're seeing this for the first time, populate the map
				// with a new entry but don't process the value. this prevents
				// reporting a spike when the agent is deployed to a long
				// running node.
				statCacheEntry := statTracker{
					recorded:    statValue,
					rateLimiter: rateLimiterConstructor(),
				}
				m.statExceededCache[cacheKey] = &statCacheEntry
			}
		}
	}
	return merr
}

func parseEthtool(ethtoolOut []byte) (map[string]int, error) {
	// Input looks like any of the following:
	// ---
	// no stats available
	// ---
	// NIC statistics:
	//      total_resets: 0
	//      reset_fail: 0
	stats := make(map[string]int)
	ethtoolLines := strings.Split(string(ethtoolOut), "\n")
	for _, line := range ethtoolLines[1:] {
		if fields := strings.Split(line, ":"); len(fields) == 2 {
			if val, err := strconv.Atoi(strings.TrimSpace(fields[1])); err == nil {
				stats[strings.TrimSpace(fields[0])] = val
			}
		}
	}
	return stats, nil
}

// ~~~~ sysctl ~~~~

func (m *NetworkingMonitor) handleNetworkSysctl() error {
	ipForward, err := osext.ParseSysctl("net.ipv4.ip_forward", func(b []byte) (int, error) { return strconv.Atoi(string(b)) })
	if err != nil {
		return err
	}
	return m.checkNetworkSysctl(*ipForward)
}

// checkNetworkSysctl validates whether the current sysctl parameters are
// configured to work correctly with kubernetes.
func (m *NetworkingMonitor) checkNetworkSysctl(ipForward int) (merr error) {
	if ipForward != 1 {
		merr = errors.Join(merr, m.manager.Notify(context.TODO(),
			reasons.NetworkSysctl.
				Builder().
				Message(fmt.Sprintf("A network related sysctl parameter may be misconfigured")).
				Build(),
		))
	}
	return nil
}

// ~~~~ ip rules/routes ~~~~

const (
	IPv4 = iota
	IPv6
)

type ipMetadata struct {
	primary bool
}

func (m *NetworkingMonitor) handleIPRulesAndRoutes() (merr error) {
	if slices.Contains(m.runtimeContext.Tags(), config.Hybrid) {
		// Hybrid nodes do not have ipamd
		return nil
	}

	enis, err := ipamd.GetEndpoint(ipamd.EndpointEnis)
	if err != nil {
		return err
	}
	rules, err := toolexec.GetRules()
	if err != nil {
		return err
	}
	routes, err := toolexec.GetRoutes()
	if err != nil {
		return err
	}

	// holds metadata about the ip address
	ipMeta := make(map[string]*ipMetadata)

	for _, eni := range enis.ENIs {
		for _, cidr := range eni.AvailableIPv4Cidrs {
			for _, addr := range cidr.IPAddresses {
				if addr.IPAMKey.ContainerID == "" || addr.IPAMKey.IfName == "" || addr.IPAMKey.NetworkName == "" {
					// if the IPAM data is not populated we dont need to track
					// this address or perform validations.
					continue
				}
				if _, ok := ipMeta[addr.Address]; !ok {
					ipMeta[addr.Address] = &ipMetadata{
						primary: eni.IsPrimary,
					}
				}
			}
		}
	}

	if len(ipMeta) == 0 {
		// exit early if there are no allocations to work with
		return nil
	}

	// derive the ip mode using the first ip from the list.
	ipMode := IPv4
	for ip := range ipMeta {
		if net.ParseIP(ip).To4() == nil {
			ipMode = IPv6
		}
		break
	}

	return errors.Join(merr,
		m.checkIPRulesAndRoutes(ipMode, ipMeta, rules, routes),
	)
}

func (m *NetworkingMonitor) checkIPRulesAndRoutes(ipMode int, ipMeta map[string]*ipMetadata, rules []string, routes []string) (merr error) {
	type ipData struct {
		ipRuleExistsErr  error
		ipRouteExistsErr error
	}
	type tableData struct {
		defaultRouteExistsErr error
	}

	tables := map[string]*tableData{}
	ips := map[string]*ipData{}

	// populate the ip data with the known ips.
	for ip := range ipMeta {
		// NOTE: <dev> cannot be substituted because IPAMD only carries
		// information about the ENI ID, which is not the same as the interface
		// name from a tool like `ip addr`.
		switch ipMode {
		case IPv4:
			ips[ip] = &ipData{
				ipRuleExistsErr:  fmt.Errorf("Expected entry like `from all to %s lookup main`", ip),
				ipRouteExistsErr: fmt.Errorf("Expected entry like `%s dev <dev> scope link`", ip),
			}
		case IPv6:
			ips[ip] = &ipData{
				ipRuleExistsErr:  fmt.Errorf("Expected entry like `from all to %s lookup main`", ip),
				ipRouteExistsErr: fmt.Errorf("Expected entry like `%s dev <dev> metric 1024 pref medium`", ip),
			}
		default:
			return fmt.Errorf("invalid ip mode")
		}
	}

	// ~ parse ip rules ~
	// this takes priority because it is where we discover the table names.

	// 512 and 1536 are the priorities assigned to these corresponding ip rules.
	// 512 will always be for pods on primary ENI and 1536 will always be for
	// pods on secondary ENIs.
	primaryEniRuleRegex := regexp.MustCompile(`512:\s+from all to (.+) lookup main`)
	secondaryEniRuleRegex := regexp.MustCompile(`1536:\s+from (.+) lookup (.+)`)

	for _, rule := range rules {
		if matches := primaryEniRuleRegex.FindStringSubmatch(rule); len(matches) >= 2 {
			ip := matches[1]
			if meta, ok := ips[ip]; ok {
				meta.ipRuleExistsErr = nil
			}
		} else if matches := secondaryEniRuleRegex.FindStringSubmatch(rule); len(matches) >= 3 {
			ip, table := matches[1], matches[2]
			if meta, ok := ips[ip]; ok {
				meta.ipRuleExistsErr = nil
			}
			// we only care about tracking default routes for secondary enis
			if _, ok := tables[table]; !ok {
				tables[table] = &tableData{
					defaultRouteExistsErr: fmt.Errorf("Expected entry like `default via <gateway-ip> dev <dev> table %s`", table),
				}
			}
		}
	}

	// ~ parse ip routes ~

	// Parse for either `<pod ip ipv4> dev enia2d278c49cc scope link` or `<pod ip ipv6> dev eni6f27e6b0279 metric 1024 pref medium`
	podIpIpv4 := regexp.MustCompile(`(.+) dev .+ scope link`)
	podIpIpv6 := regexp.MustCompile(`(.+) dev .+ metric 1024 pref medium`)
	// Parse for `default via <ip> dev eth1 table <table id>`
	defaultRouteRegex := regexp.MustCompile(`default via .+ dev .+ table (\w+)`)

	for _, route := range routes {
		switch ipMode {
		case IPv4:
			if matches := podIpIpv4.FindStringSubmatch(route); len(matches) >= 2 {
				ip := matches[1]
				if meta, ok := ips[ip]; ok {
					meta.ipRouteExistsErr = nil
				}
			}
			// default routes only apply in IPv4
			if matches := defaultRouteRegex.FindStringSubmatch(route); len(matches) >= 2 {
				tableName := matches[1]
				if table, ok := tables[tableName]; ok {
					table.defaultRouteExistsErr = nil
				}
			}
		case IPv6:
			if matches := podIpIpv6.FindStringSubmatch(route); len(matches) >= 2 {
				ip := matches[1]
				if meta, ok := ips[ip]; ok {
					meta.ipRouteExistsErr = nil
				}
			}
		default:
			return fmt.Errorf("invalid ip mode")
		}
	}

	// ~ evaluate the results of the ips ~

	for ip, data := range ips {
		var item string
		if meta, ok := ipMeta[ip]; ok && meta.primary {
			item = "secondary "
		}
		if data.ipRuleExistsErr != nil {
			item += "rules"
			merr = errors.Join(merr, m.manager.Notify(context.TODO(),
				reasons.MissingIPRules.
					Builder().
					Message(fmt.Sprintf("Pod IP %s is missing %s: %s", ip, item, data.ipRuleExistsErr)).
					Build(),
			))
		} else if data.ipRouteExistsErr != nil {
			item += "routes"
			merr = errors.Join(merr, m.manager.Notify(context.TODO(),
				reasons.MissingIPRoutes.
					Builder().
					Message(fmt.Sprintf("Pod IP %s is missing %s: %s", ip, item, data.ipRouteExistsErr)).
					Build(),
			))
		}
	}

	// ~ evaluate the results of the table queries ~

	for tableName, table := range tables {
		if table.defaultRouteExistsErr != nil {
			merr = errors.Join(merr, m.manager.Notify(context.TODO(),
				reasons.MissingDefaultRoutes.
					Builder().
					Message(fmt.Sprintf("Missing default route rules for table %s: %s", tableName, table.defaultRouteExistsErr)).
					Build(),
			))
		}
	}

	return merr
}

// ~~~~ ip tables ~~~~

func (m *NetworkingMonitor) handleIPTables() (merr error) {
	// parses output from iptables-save in order to assemble a list of the rules
	// which are allowing/denying packets to the instance.
	var rules []iptables.IPTablesRule
	for _, cmd := range []string{"iptables-save", "ip6tables-save"} {
		out, err := osext.NewExec(config.HostRoot()).Command(cmd).CombinedOutput()
		if err != nil {
			merr = errors.Join(merr, fmt.Errorf("failed command %q: %w", cmd, err))
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			// only look at rule lines
			if !strings.HasPrefix(line, "-A") {
				continue
			}
			if rule, err := iptables.ParseIPTablesRule(line); err != nil {
				merr = errors.Join(merr, err)
			} else {
				rules = append(rules, *rule)
			}
		}
	}
	return errors.Join(merr,
		m.checkIPTables(rules),
	)
}

func (m *NetworkingMonitor) checkIPTables(rules []iptables.IPTablesRule) (merr error) {
	checkRejectRule := func(rule iptables.IPTablesRule) error {
		// detects whenever there is a REJECT rule in iptables which is not
		// expected. These can cause traffic to incorrectly be blocked, and are
		// often part of some security-related third-party agent.
		if rule.IsReject() && !rule.IsExpectedRejectRule() {
			merr = errors.Join(merr, m.manager.Notify(context.TODO(),
				reasons.UnexpectedRejectRule.
					Builder().
					Message(fmt.Sprintf("Found an unexpected iptables reject rule: %q", rule.String())).
					Build(),
			))
		}
		return nil
	}

	checkPortConflict := func(rule iptables.IPTablesRule) error {
		// checks for when iptables rules are clobbering known ports on the
		// host, which has happenned in the past where the api server could not
		// reach kubelet's endpoint.
		knownPorts := map[int]string{
			10250: "kubelet",
		}
		// skip rules without dports
		if rule.DestinationPort == "" {
			return nil
		}
		dport, err := strconv.Atoi(rule.DestinationPort)
		if err != nil {
			return fmt.Errorf("parsing dport, %w", err)
		}
		if _, ok := knownPorts[dport]; ok && rule.Jump == "DNAT" {
			merr = errors.Join(merr, m.manager.Notify(context.TODO(),
				reasons.PortConflict.
					Builder().
					Message(fmt.Sprintf("Potential iptables rule conflict on port %s", rule.DestinationPort)).
					Build(),
			))
		}
		return nil
	}

	for _, rule := range rules {
		merr = errors.Join(merr,
			checkPortConflict(rule),
		)
		if !slices.Contains(m.runtimeContext.Tags(), config.Hybrid) {
			// check reject rule if it is not hybrid node
			// customer manages hybrid node, thus can have customized reject rules
			merr = errors.Join(merr,
				checkRejectRule(rule),
			)
		}
	}
	return merr
}

// ~~~~ MAC Address Policy ~~~~

func (m *NetworkingMonitor) handleMACAddressPolicy() error {
	// Only check MAC address policy for VPC CNI on Ubuntu nodes
	if !m.isMACAddressPolicyCheckNeeded(m.runtimeContext) {
		m.log.Info("Skipping MAC address policy check - not needed for this OS")
		return nil
	}

	if !m.isVPCCNIInstalled() {
		m.log.Info("Skipping MAC address policy check - VPC CNI is not installed")
		return nil
	}

	interfaces, err := networkutils.GetNetworkInterfaces(m.exec)
	if err != nil {
		m.log.Error(err, "Failed to get network interfaces")
		return err
	}

	// Collect unique LinkFiles
	linkFiles := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.LinkFile != "" {
			linkFiles[iface.LinkFile] = true
		}
	}

	m.log.Info("Processing unique LinkFiles", "count", len(linkFiles))
	for linkFile := range linkFiles {
		m.log.Info("Checking MAC address policy", "linkFile", linkFile)
		// Get the effective configuration for this link file
		configOutput, err := m.exec.Command("systemd-analyze", "cat-config", linkFile).CombinedOutput()
		if err != nil {
			m.log.Info("Failed to get config for LinkFile", "linkFile", linkFile, "error", err)
			continue
		}

		if err := m.checkMACAddressPolicy(string(configOutput), linkFile); err != nil {
			return err
		}
	}
	return nil
}

// isVPCCNIInstalled checks if AWS VPC CNI is installed by looking for aws-node pods
func (m *NetworkingMonitor) isVPCCNIInstalled() bool {
	dirs, err := os.ReadDir(config.PodLogsDirPath)
	if err != nil {
		m.log.Info("Failed to read pod logs directory", "error", err)
		return false
	}
	for _, path := range dirs {
		if strings.Contains(path.Name(), "aws-node") {
			m.log.Info("VPC CNI detected", "podDir", path.Name())
			return true
		}
	}
	m.log.Info("VPC CNI not detected - no aws-node pods found")
	return false
}

func (m *NetworkingMonitor) checkMACAddressPolicy(content, fileName string) error {
	currentPolicy, isHealthy := networkutils.CheckMACAddressPolicy(content)
	if !isHealthy {
		return m.manager.Notify(context.Background(),
			reasons.MACAddressPolicyMisconfigured.
				Builder().
				Message(fmt.Sprintf("MACAddressPolicy in %s is set to %q instead of 'none', which may cause network issues", fileName, currentPolicy)).
				Build(),
		)
	}
	return nil
}

// bottleRocket and Amazon Linux have a safe MAC address policy
func (m *NetworkingMonitor) isMACAddressPolicyCheckNeeded(runtimeContext *config.RuntimeContext) bool {
	return slices.Contains(runtimeContext.Tags(), config.UbuntuDistro)
}
