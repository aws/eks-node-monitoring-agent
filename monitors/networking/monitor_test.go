package networking

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/tools/cache"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/iptables"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

type mockManager struct {
	err error
	obs observer.BaseObserver
	res chan monitor.Condition
}

type mockExec struct {
	commands map[string]mockCommand
}

type mockCommand struct {
	output []byte
	err    error
}

func (m *mockExec) Command(name string, args ...string) *exec.Cmd {
	cmdKey := name + " " + strings.Join(args, " ")

	// Create a command that will be executed via a shell script
	// This is a simple way to mock the command execution
	if mockCmd, ok := m.commands[cmdKey]; ok {
		if mockCmd.err != nil {
			// Return a command that will fail
			return exec.Command("false")
		}
		// Create a command that outputs the expected result
		return exec.Command("echo", "-n", string(mockCmd.output))
	}

	// Command not found, return a failing command
	return exec.Command("false")
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.res <- condition
	return nil
}

func TestNetworkingMonitor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, testCase := range []struct {
		log    string
		reason string
		monitor.Severity
	}{
		{"Failed to check API server connectivity: invalid configuration: no configuration has been provided", "IPAMDNotReady", monitor.SeverityFatal},
		{"Unable to reach API Server", "IPAMDNotReady", monitor.SeverityFatal},
		{"Failed to check API server connectivity", "IPAMDNotReady", monitor.SeverityFatal},
		{"Starting L-IPAMD", "IPAMDRepeatedlyRestart", monitor.SeverityWarning},
		{"InsufficientFreeAddressesInSubnet", "IPAMDNoIPs", monitor.SeverityWarning},
	} {
		t.Run(testCase.log, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			mon := NewNetworkingMonitor()
			mockManager := mockManager{
				obs: observer.BaseObserver{},
				res: make(chan monitor.Condition, 5),
			}
			mon.Register(ctx, &mockManager)
			mockManager.obs.Broadcast("mock", testCase.log)
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case monitorResult := <-mockManager.res:
				assert.Equal(t, testCase.Severity, monitorResult.Severity)
				assert.Equal(t, testCase.reason, monitorResult.Reason)
			}
		})
	}

	t.Run("SubscribeError", func(t *testing.T) {
		networkingMonitor := NewNetworkingMonitor()
		mockError := fmt.Errorf("mock error")
		mockManager := &mockManager{err: mockError, obs: observer.BaseObserver{}}
		assert.NotNil(t, networkingMonitor.Register(ctx, mockManager))
	})
}

func TestNetworkingPeriodic(t *testing.T) {
	for _, testCase := range []struct {
		caseName                 string
		flags                    net.Flags
		notificationOnFirstCycle bool
	}{
		{"InterfaceNotUp", net.FlagRunning | net.FlagLoopback, false},
		{"InterfaceNotRunning", net.FlagUp | net.FlagLoopback, false},
		{"MissingLoopbackInterface", net.FlagUp | net.FlagRunning, true},
	} {
		t.Run(testCase.caseName, func(t *testing.T) {
			mon := NewNetworkingMonitor()
			mockManager := &mockManager{
				obs: observer.BaseObserver{},
				res: make(chan monitor.Condition, 5),
			}
			mon.Register(context.TODO(), mockManager)
			interfaces := []net.Interface{{Flags: testCase.flags}}

			if err := mon.checkInterfaces(interfaces); err != nil {
				t.Fatal(err)
			}
			select {
			case monitorResult := <-mockManager.res:
				if testCase.notificationOnFirstCycle {
					assert.Equal(t, testCase.caseName, monitorResult.Reason)
					return
				} else {
					t.Fatalf("unexpected notification after first checkInterfaces cycle: %v", monitorResult)
				}
			default:
				if testCase.notificationOnFirstCycle {
					t.Fatalf("missing notification after first checkInterfaces cycle")
				}
			}

			if err := mon.checkInterfaces(interfaces); err != nil {
				t.Fatal(err)
			}
			select {
			case monitorResult := <-mockManager.res:
				assert.Equal(t, testCase.caseName, monitorResult.Reason)
			default:
				t.Fatalf("missing notification after second checkInterfaces cycle")
			}
		})
	}

	t.Run("PortConflict", func(t *testing.T) {
		rule, err := iptables.ParseIPTablesRule(`-A CNI-DN-08cb4d05ff8daf230ce42 -p tcp -m tcp --dport 10250 -j DNAT --to-destination 172.31.106.34:10250`)
		assert.NoError(t, err)
		rules := []iptables.IPTablesRule{*rule}

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		if !assert.NoError(t, mon.checkIPTables(rules)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "PortConflict", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	for _, ruleRaw := range []string{
		`-A SOMETHING-ELSE -m comment -j DROP`,
		`-A TWISTLOCK-NET-OUTPUT -s 1.2.3.4/26 -p tcp -m tcp --tcp-flags FIN,SYN,RST,ACK SYN -m mark --mark 0x10101010 -m comment --comment TWISTLOCK-RULE -j REJECT --reject-with tcp-reset`,
	} {
		rule, err := iptables.ParseIPTablesRule(ruleRaw)
		assert.NoError(t, err)
		rules := []iptables.IPTablesRule{*rule}

		t.Run("UnexpectedRejectRule", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			mon := NewNetworkingMonitor()
			mockManager := &mockManager{
				obs: observer.BaseObserver{},
				res: make(chan monitor.Condition, 5),
			}
			mon.Register(ctx, mockManager)
			if !assert.NoError(t, mon.checkIPTables(rules)) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case monitorResult := <-mockManager.res:
				assert.Equal(t, "UnexpectedRejectRule", monitorResult.Reason)
				assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			}
		})
	}

	t.Run("IPAMDNotRunning", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		if !assert.NoError(t, mon.checkIPAMD(true, false)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "IPAMDNotRunning", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityFatal, monitorResult.Severity)
		}
	})

	t.Run("EthtoolCheck", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		ethtoolMonitor := makeEthtoolMonitor(mockManager)
		mon.Register(ctx, mockManager)
		ethtoolBytes, err := os.ReadFile("testdata/ethtool-ens5.txt")
		if !assert.NoError(t, err) {
			return
		}
		stats, err := parseEthtool(ethtoolBytes)
		if !assert.NoError(t, err) {
			return
		}
		ethtoolMonitor.checkEthtool("ens5", stats)
		stats[BandwidthInExceeded] = stats[BandwidthInExceeded] + 5000
		if !assert.NoError(t, ethtoolMonitor.checkEthtool("ens5", stats)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "BandwidthInExceeded", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("NetworkSysctl", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		if !assert.NoError(t, mon.checkNetworkSysctl(0)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "NetworkSysctl", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("BadIPMode", func(t *testing.T) {
		mon := NewNetworkingMonitor()
		assert.Error(t, mon.checkIPRulesAndRoutes(
			99,
			map[string]*ipMetadata{},
			[]string{},
			[]string{`anything. just need to run at least one rule`},
		))
	})

	t.Run("MissingDefaultRoutes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		// rules will pick up the table name, and there should be no matching
		// default in the routes list. no ips are needed.
		if !assert.NoError(t, mon.checkIPRulesAndRoutes(
			IPv4,
			map[string]*ipMetadata{},
			[]string{`1536:	from 192.168.55.179 lookup 3`},
			[]string{},
		)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MissingDefaultRoutes", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MACAddressPolicyNoPolicy", func(t *testing.T) {
		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(context.TODO(), mockManager)

		// Should skip files without MACAddressPolicy
		err := mon.checkMACAddressPolicy("[Network]\nDHCP=yes", "test.link")
		assert.NoError(t, err)

		// Should not send any event
		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event for file without MACAddressPolicy: %+v", result)
		default:
			// Expected no event
		}
	})

	t.Run("MACAddressPolicyNone", func(t *testing.T) {
		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(context.TODO(), mockManager)

		// MACAddressPolicy=none should not send any event
		err := mon.checkMACAddressPolicy("MACAddressPolicy=none", "test.link")
		assert.NoError(t, err)

		// Should not send any event
		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event for MACAddressPolicy=none: %+v", result)
		default:
			// Expected no event
		}
	})

	t.Run("MACAddressPolicyEmpty", func(t *testing.T) {
		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(context.TODO(), mockManager)

		// Empty value should be treated as 'none' (healthy) - no event
		err := mon.checkMACAddressPolicy("MACAddressPolicy=", "test.link")
		assert.NoError(t, err)

		// Should not send any event
		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event for empty MACAddressPolicy: %+v", result)
		default:
			// Expected no event
		}
	})

	t.Run("MACAddressPolicyMisconfigured", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)

		err := mon.checkMACAddressPolicy("MACAddressPolicy=persistent", "99-default.link")
		assert.NoError(t, err)

		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MACAddressPolicyMisconfigured", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MissingIPRules-IPv4", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		// we need a single ip that has no corresponding rules.
		// because we have an ip, we need a route.
		if !assert.NoError(t, mon.checkIPRulesAndRoutes(
			IPv4,
			map[string]*ipMetadata{"mock-ip": {primary: false}},
			[]string{`1536:	from 192.168.55.179 lookup mytable`},
			[]string{
				`mock-ip dev eni6f7ecb4a8e8 scope link`,
				`default via 192.168.32.1 dev ens7 table mytable `,
			},
		)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MissingIPRules", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MissingIPRules-IPv6", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		// we need a single ip that has no corresponding rules.
		// because we have an ip, we need a route.
		if !assert.NoError(t, mon.checkIPRulesAndRoutes(
			IPv6,
			map[string]*ipMetadata{"mock-ip": {primary: false}},
			[]string{`1536:	from 192.168.55.179 lookup 3`},
			[]string{
				`mock-ip dev (...) metric 1024 pref medium`,
				`default via 192.168.32.1 dev ens7 table 3 `,
			},
		)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MissingIPRules", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MissingIPRoutes-IPv4", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		// we need a single ip that has no corresponding routes.
		// because we have an ip, we need a rule.
		if !assert.NoError(t, mon.checkIPRulesAndRoutes(
			IPv4,
			map[string]*ipMetadata{"mock-ip": {primary: true}},
			[]string{`1536:	from mock-ip lookup 3`},
			[]string{`default via 192.168.32.1 dev ens7 table 3`},
		)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MissingIPRoutes", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MissingIPRoutes-IPv6", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mon := NewNetworkingMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		// we need a single ip that has no corresponding routes.
		// because we have an ip, we need a rule.
		if !assert.NoError(t, mon.checkIPRulesAndRoutes(
			IPv6,
			map[string]*ipMetadata{"mock-ip": {primary: true}},
			[]string{`1536:	from mock-ip lookup 3`},
			[]string{`default via 192.168.32.1 dev ens7 table 3`},
		)) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MissingIPRoutes", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})
}

func TestHandleMACAddressPolicy(t *testing.T) {
	t.Run("SkipWhenNotUbuntu", func(t *testing.T) {
		rtCtx := &config.RuntimeContext{}
		mon := NewNetworkingMonitor(WithRuntimeContext(rtCtx))
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(context.TODO(), mockManager)

		err := mon.handleMACAddressPolicy()
		assert.NoError(t, err)

		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event when MAC policy check should be skipped for non-Ubuntu: %+v", result)
		default:
			// Expected no event
		}
	})

	t.Run("SkipWhenVPCCNINotInstalled", func(t *testing.T) {
		// Create empty pod logs directory (no aws-node directories)
		tmpDir := t.TempDir()
		originalPodLogsDir := config.PodLogsDirPath
		config.PodLogsDirPath = tmpDir
		defer func() { config.PodLogsDirPath = originalPodLogsDir }()

		rtCtx := &config.RuntimeContext{}
		rtCtx.AddTags(config.UbuntuDistro)
		mon := NewNetworkingMonitor(WithRuntimeContext(rtCtx))
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(context.TODO(), mockManager)

		// No aws-node directory in pod logs - VPC CNI not installed
		err := mon.handleMACAddressPolicy()
		assert.NoError(t, err)

		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event when VPC CNI not installed: %+v", result)
		default:
			// Expected no event
		}
	})

	t.Run("MACAddressPolicyPersistent", func(t *testing.T) {
		// Test case 3: networkctl returns valid interface list and MACAddressPolicy=persistent
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mockExec := &mockExec{
			commands: map[string]mockCommand{
				"networkctl --json=short": {
					output: []byte(`{"Interfaces":[{"Name":"eth0","LinkFile":"99-default.link"}]}`),
					err:    nil,
				},
				"systemd-analyze cat-config 99-default.link": {
					output: []byte("MACAddressPolicy=persistent"),
					err:    nil,
				},
			},
		}

		// Create mock pod logs directory with aws-node
		tmpDir := t.TempDir()
		awsNodeDir := filepath.Join(tmpDir, "kube-system_aws-node-12345")
		os.MkdirAll(awsNodeDir, 0755)
		originalPodLogsDir := config.PodLogsDirPath
		config.PodLogsDirPath = tmpDir
		defer func() { config.PodLogsDirPath = originalPodLogsDir }()

		rtCtx := &config.RuntimeContext{}
		rtCtx.AddTags(config.UbuntuDistro)
		mon := NewNetworkingMonitor(WithExec(mockExec), WithRuntimeContext(rtCtx))
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)

		err := mon.handleMACAddressPolicy()
		assert.NoError(t, err)

		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "MACAddressPolicyMisconfigured", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("MACAddressPolicyNone", func(t *testing.T) {
		// Test case 4: networkctl returns valid interface list and MACAddressPolicy=none
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		mockExec := &mockExec{
			commands: map[string]mockCommand{
				"networkctl --json=short": {
					output: []byte(`{"Interfaces":[{"Name":"eth0","LinkFile":"99-default.link"}]}`),
					err:    nil,
				},
				"systemd-analyze cat-config 99-default.link": {
					output: []byte("MACAddressPolicy=none"),
					err:    nil,
				},
			},
		}

		// Create mock pod logs directory with aws-node
		tmpDir := t.TempDir()
		awsNodeDir := filepath.Join(tmpDir, "kube-system_aws-node-12345")
		os.MkdirAll(awsNodeDir, 0755)
		originalPodLogsDir := config.PodLogsDirPath
		config.PodLogsDirPath = tmpDir
		defer func() { config.PodLogsDirPath = originalPodLogsDir }()

		rtCtx := &config.RuntimeContext{}
		rtCtx.AddTags(config.UbuntuDistro)
		mon := NewNetworkingMonitor(WithExec(mockExec), WithRuntimeContext(rtCtx))
		mockManager := &mockManager{
			res: make(chan monitor.Condition, 1),
		}
		mon.Register(ctx, mockManager)

		err := mon.handleMACAddressPolicy()
		assert.NoError(t, err)

		// Should receive no error and no notification
		select {
		case result := <-mockManager.res:
			t.Fatalf("unexpected event for MACAddressPolicy=none: %+v", result)
		default:
			// Expected no event
		}
	})
}

func TestIsVPCCNIInstalled(t *testing.T) {
	t.Run("VPCCNINotInstalled", func(t *testing.T) {
		mon := NewNetworkingMonitor()
		result := mon.isVPCCNIInstalled()
		assert.False(t, result)
	})
}

func TestCheckIPAMD_CacheExpiry(t *testing.T) {
	// Regression test: cache entry exists on first check but expires before second check.
	// The code must not panic on type assertion when GetByKey returns exists=false.
	tmpDir := t.TempDir()
	t.Setenv("HOST_ROOT", tmpDir)

	// Create checkpoint file
	checkpointDir := filepath.Join(tmpDir, "var", "run", "aws-node")
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		t.Fatal(err)
	}
	checkpointData := `{"allocations":[{"containerID":"test-container","ipv4":"10.0.0.1"}]}`
	if err := os.WriteFile(filepath.Join(checkpointDir, "ipam.json"), []byte(checkpointData), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	mon := NewNetworkingMonitor()
	mockManager := &mockManager{
		obs: observer.BaseObserver{},
		res: make(chan monitor.Condition, 5),
	}
	mon.Register(ctx, mockManager)

	// Pre-populate cache so first GetByKey returns ok=true
	mon.criIPCache.Add(criIPDetails{
		ip:          "10.0.0.1",
		containerId: "test-container",
		name:        "test-pod",
		namespace:   "default",
	})

	// Replace cache with one that returns exists=false on second call (simulating expiry)
	mon.criIPCache = &expiringCache{
		Store:      mon.criIPCache,
		expireNext: true,
	}

	// This should not panic even if cache entry "expires" between checks
	err := mon.checkIPAMD(true, true)
	assert.NoError(t, err)
}

// expiringCache wraps a cache.Store and simulates entry expiry
type expiringCache struct {
	cache.Store
	calls      int
	expireNext bool
}

func (c *expiringCache) GetByKey(key string) (interface{}, bool, error) {
	c.calls++
	if c.calls == 1 {
		// First call: entry exists
		return c.Store.GetByKey(key)
	}
	// Second call: simulate expiry
	if c.expireNext {
		return nil, false, nil
	}
	return c.Store.GetByKey(key)
}

func TestUtils(t *testing.T) {
	t.Run("EthtoolParseNoStats", func(t *testing.T) {
		ethtoolBytes, err := os.ReadFile("testdata/ethtool-lo.txt")
		if !assert.NoError(t, err) {
			return
		}
		stats, err := parseEthtool(ethtoolBytes)
		if !assert.NoError(t, err) {
			return
		}
		assert.Empty(t, stats)
	})

	t.Run("EthtoolParse", func(t *testing.T) {
		ethtoolBytes, err := os.ReadFile("testdata/ethtool-ens5.txt")
		if !assert.NoError(t, err) {
			return
		}
		stats, err := parseEthtool(ethtoolBytes)
		if !assert.NoError(t, err) {
			return
		}
		assert.Len(t, stats, 115)
		assert.Equal(t, 11072, stats[BandwidthInExceeded])
	})
}
