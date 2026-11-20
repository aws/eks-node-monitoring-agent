package networking

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

// fakeENIs maps interface names to ENI IDs.
type fakeENIs map[string]string

func (enis fakeENIs) ENIID(_ context.Context, interfaceName string) (string, error) {
	if eniID, ok := enis[interfaceName]; ok {
		return eniID, nil
	}
	return "", fmt.Errorf("no ENI for %q", interfaceName)
}

func ens5Stats(t *testing.T) map[string]int {
	t.Helper()
	ethtoolBytes, err := os.ReadFile("testdata/ethtool-ens5.txt")
	require.NoError(t, err)
	stats, err := parseEthtool(ethtoolBytes)
	require.NoError(t, err)
	return stats
}

func TestEthtoolLinkLocalInsight(t *testing.T) {
	stats := ens5Stats(t)
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	ethtoolMonitor := makeEthtoolMonitor(mockManager, fakeENIs{"ens5": "eni-ens5"})
	now := time.Now()
	ethtoolMonitor.now = func() time.Time { return now }
	handler := ethtoolTickHandler(context.Background(), ethtoolMonitor, func() (map[string]map[string]int, error) {
		return map[string]map[string]int{"ens5": stats}, nil
	})
	warnings := func() int {
		count := len(mockManager.res)
		for len(mockManager.res) > 0 {
			<-mockManager.res
		}
		return count
	}
	check := func(increase int) {
		stats[LinkLocalExceeded] += increase
		require.NoError(t, handler(now))
	}

	check(0)
	assert.Empty(t, mockManager.insights, "the first reading is only a baseline")
	assert.Positive(t, mockManager.replacedGroups, "but the reconcile still ran")

	check(100000)
	assert.Equal(t, 1, warnings())
	assert.Equal(t, map[string]monitor.Insight{"eni-ens5": {
		Type:         "NetworkAllowanceExceeded",
		Reason:       "LinkLocal",
		ResourceName: "eni_id",
	}}, mockManager.insights, "a spike publishes the ENI's insight")

	now = now.Add(ethtoolMonitorPeriod)
	check(100000)
	assert.Zero(t, warnings(), "the Warning is suppressed within its re-notify interval")
	assert.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs(), "but the insight stays while the spike continues")

	now = now.Add(ethtoolMonitorPeriod)
	check(0)
	assert.Empty(t, mockManager.insights, "a check without a spike clears the insight")
}

func TestEthtoolLinkLocalInsight_TwoENIs(t *testing.T) {
	ens5, ens6 := ens5Stats(t), ens5Stats(t)
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	ethtoolMonitor := makeEthtoolMonitor(mockManager, fakeENIs{"ens5": "eni-ens5", "ens6": "eni-ens6"})
	now := time.Now()
	ethtoolMonitor.now = func() time.Time { return now }
	handler := ethtoolTickHandler(context.Background(), ethtoolMonitor, func() (map[string]map[string]int, error) {
		return map[string]map[string]int{"ens5": ens5, "ens6": ens6}, nil
	})
	check := func(ens5Increase, ens6Increase int) {
		ens5[LinkLocalExceeded] += ens5Increase
		ens6[LinkLocalExceeded] += ens6Increase
		require.NoError(t, handler(now))
	}

	check(0, 0)
	assert.Empty(t, mockManager.insights, "the first reading is only a baseline")
	assert.Positive(t, mockManager.replacedGroups, "but the reconcile still ran")

	check(100000, 0)
	assert.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs(), "only the spiking ENI has an insight")

	check(100000, 100000)
	assert.ElementsMatch(t, []string{"eni-ens5", "eni-ens6"}, mockManager.insightENIIDs(),
		"each ENI exceeding its allowance has its own insight")
	assert.Equal(t, monitor.Insight{
		Type:         insightTypeNetworkAllowanceExceeded,
		Reason:       insightReasonLinkLocal,
		ResourceName: insightResourceENIID,
	}, mockManager.insights["eni-ens6"], "the insight is keyed by the ENI it's about")

	// ens5 recovers while ens6 keeps spiking
	now = now.Add(ethtoolMonitorPeriod)
	check(0, 100000)
	assert.Equal(t, []string{"eni-ens6"}, mockManager.insightENIIDs(),
		"the recovered ENI's insight clears while the other one's stays")
}

func TestEthtoolLinkLocalInsight_InterfaceGone(t *testing.T) {
	baseline := ens5Stats(t)
	spiked := ens5Stats(t)
	spiked[LinkLocalExceeded] += 100000
	readings := []map[string]map[string]int{{"ens5": baseline}, {"ens5": spiked}, {}}
	tick := 0
	reader := func() (map[string]map[string]int, error) {
		reading := readings[tick]
		tick++
		return reading, nil
	}
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	handler := ethtoolTickHandler(context.Background(), makeEthtoolMonitor(mockManager, fakeENIs{"ens5": "eni-ens5"}), reader)

	require.NoError(t, handler(time.Now()))
	require.NoError(t, handler(time.Now()))
	assert.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs())
	require.NoError(t, handler(time.Now()))
	assert.Empty(t, mockManager.insights, "an interface that's no longer read clears its insight")
}

func TestEthtoolLinkLocalInsight_ENILookupFails(t *testing.T) {
	stats := ens5Stats(t)
	enis := fakeENIs{}
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	handler := ethtoolTickHandler(context.Background(), makeEthtoolMonitor(mockManager, enis), func() (map[string]map[string]int, error) {
		return map[string]map[string]int{"ens5": stats}, nil
	})
	spike := func() error {
		stats[LinkLocalExceeded] += 100000
		return handler(time.Now())
	}

	require.NoError(t, handler(time.Now()))
	assert.ErrorContains(t, spike(), `no ENI for "ens5"`)
	assert.ErrorContains(t, spike(), `no ENI for "ens5"`, "every failure is reported, the lookup isn't given up on")
	assert.Empty(t, mockManager.insights, "no insight is published without an ENI ID")
	assert.Len(t, mockManager.res, 1, "the Warning is sent once, within its re-notify interval")

	// the ENI resolves now, e.g. IMDS became reachable
	enis["ens5"] = "eni-ens5"
	require.NoError(t, spike())
	assert.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs(), "the insight publishes once the ENI resolves")
}

// fakeIMDS serves interface IDs by IMDS path and counts the requests.
type fakeIMDS struct {
	paths    map[string]string
	requests int
}

func (metadata *fakeIMDS) GetMetadata(_ context.Context, params *imds.GetMetadataInput, _ ...func(*imds.Options)) (*imds.GetMetadataOutput, error) {
	metadata.requests++
	value, ok := metadata.paths[params.Path]
	if !ok {
		return nil, errors.New("not found")
	}
	return &imds.GetMetadataOutput{Content: io.NopCloser(strings.NewReader(value + "\n"))}, nil
}

func TestIMDSENIResolver(t *testing.T) {
	metadata := &fakeIMDS{paths: map[string]string{
		"network/interfaces/macs/02:00:00:00:00:01/interface-id": "eni-0123456789abcdef0",
	}}
	macAddresses := map[string]string{"ens5": "02:00:00:00:00:01"}
	resolver := newIMDSENIResolver()
	resolver.client = metadata
	resolver.macAddress = func(interfaceName string) (string, error) {
		if mac, ok := macAddresses[interfaceName]; ok {
			return mac, nil
		}
		return "", errors.New("no such interface")
	}

	eniID, err := resolver.ENIID(context.Background(), "ens5")
	require.NoError(t, err)
	assert.Equal(t, "eni-0123456789abcdef0", eniID)
	_, err = resolver.ENIID(context.Background(), "ens5")
	require.NoError(t, err)
	assert.Equal(t, 1, metadata.requests, "the ENI ID is cached by interface name")

	// the ENI was replaced, so the interface's name is now a different one's
	macAddresses["ens5"] = "02:00:00:00:00:02"
	metadata.paths["network/interfaces/macs/02:00:00:00:00:02/interface-id"] = "eni-00000000000000002"
	eniID, err = resolver.ENIID(context.Background(), "ens5")
	require.NoError(t, err)
	assert.Equal(t, "eni-00000000000000002", eniID, "a new MAC address on the same interface is resolved again")
	assert.Equal(t, 2, metadata.requests)

	_, err = resolver.ENIID(context.Background(), "ens6")
	assert.ErrorContains(t, err, "MAC address")

	macAddresses["ens5"] = "02:00:00:00:00:03"
	_, err = resolver.ENIID(context.Background(), "ens5")
	assert.ErrorContains(t, err, "interface-id", "an unknown MAC address isn't in IMDS")
}

func TestEthtoolLinkLocalInsight_ReadFailureKeepsInsight(t *testing.T) {
	stats := ens5Stats(t)
	stats[LinkLocalExceeded] += 100000
	readable := true
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	handler := ethtoolTickHandler(context.Background(), makeEthtoolMonitor(mockManager, fakeENIs{"ens5": "eni-ens5"}), func() (map[string]map[string]int, error) {
		if !readable {
			return nil, errors.New("ethtool is wedged")
		}
		return map[string]map[string]int{"ens5": stats}, nil
	})

	require.NoError(t, handler(time.Now())) // baseline
	stats[LinkLocalExceeded] += 100000
	require.NoError(t, handler(time.Now()))
	require.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs(), "the spike publishes the insight")

	readable = false
	assert.Error(t, handler(time.Now()), "the read failure is reported")
	assert.Equal(t, []string{"eni-ens5"}, mockManager.insightENIIDs(),
		"a check that read nothing leaves the insight alone rather than reporting a recovery")
}

func TestIMDSENIResolver_WarmedBeforeSpike(t *testing.T) {
	stats := ens5Stats(t)
	metadata := &fakeIMDS{paths: map[string]string{
		"network/interfaces/macs/02:00:00:00:00:01/interface-id": "eni-0123456789abcdef0",
	}}
	resolver := newIMDSENIResolver()
	resolver.client = metadata
	resolver.macAddress = func(string) (string, error) { return "02:00:00:00:00:01", nil }

	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	ethtoolMonitor := makeEthtoolMonitor(mockManager, resolver)

	// the baseline check publishes nothing, but must still reach IMDS, so that
	// the spike never has to while the link-local allowance is exceeded
	require.NoError(t, ethtoolMonitor.checkEthtool(context.Background(), "ens5", stats, noInsights))
	require.Empty(t, mockManager.insights, "the first reading is only a baseline")
	assert.Equal(t, 1, metadata.requests, "the ENI is resolved before any spike")

	stats[LinkLocalExceeded] += 100000
	require.NoError(t, ethtoolMonitor.checkEthtool(context.Background(), "ens5", stats, func(_ monitor.Insight, eniID string) {
		assert.Equal(t, "eni-0123456789abcdef0", eniID)
	}))
	assert.Equal(t, 1, metadata.requests, "and the spike path reads the cache, not IMDS")
}

func TestIMDSENIResolver_RejectsNonENI(t *testing.T) {
	for name, served := range map[string]string{
		"empty":     "",
		"truncated": "eni-",
		"html":      "<html>error</html>",
		"wrongKind": "subnet-0123456789abcdef0",
	} {
		t.Run(name, func(t *testing.T) {
			metadata := &fakeIMDS{paths: map[string]string{
				"network/interfaces/macs/02:00:00:00:00:01/interface-id": served,
			}}
			resolver := newIMDSENIResolver()
			resolver.client = metadata
			resolver.macAddress = func(string) (string, error) { return "02:00:00:00:00:01", nil }

			_, err := resolver.ENIID(context.Background(), "ens5")
			assert.ErrorContains(t, err, "is not an ENI ID")
			_, err = resolver.ENIID(context.Background(), "ens5")
			assert.ErrorContains(t, err, "is not an ENI ID", "and the bad value is not cached")
			assert.Equal(t, 2, metadata.requests, "so the lookup is retried")
		})
	}
}

func TestEthtoolLinkLocalInsight_ReadFailureSkipsReconcile(t *testing.T) {
	mockManager := &mockManager{obs: observer.BaseObserver{}, res: make(chan monitor.Condition, 10)}
	handler := ethtoolTickHandler(context.Background(), makeEthtoolMonitor(mockManager, fakeENIs{}), func() (map[string]map[string]int, error) {
		return nil, errors.New("ethtool is wedged")
	})
	assert.Error(t, handler(time.Now()))
	assert.Zero(t, mockManager.replacedGroups,
		"a check that read nothing must not reconcile, or it reports a recovery it did not observe")
}

func TestIMDSENIResolver_CachedThenMACLookupFails(t *testing.T) {
	metadata := &fakeIMDS{paths: map[string]string{
		"network/interfaces/macs/02:00:00:00:00:01/interface-id": "eni-0123456789abcdef0",
	}}
	resolver := newIMDSENIResolver()
	resolver.client = metadata
	readable := true
	resolver.macAddress = func(string) (string, error) {
		if !readable {
			return "", errors.New("no such interface")
		}
		return "02:00:00:00:00:01", nil
	}

	eniID, err := resolver.ENIID(context.Background(), "ens5")
	require.NoError(t, err)
	require.Equal(t, "eni-0123456789abcdef0", eniID)

	// the interface goes away, so its MAC can no longer be read. the cached ENI
	// must not be served: the cache is only trusted while the MAC confirms the
	// same ENI is still attached, and a detached one is no longer exceeding
	// anything.
	readable = false
	_, err = resolver.ENIID(context.Background(), "ens5")
	assert.ErrorContains(t, err, "MAC address")
	assert.Equal(t, 1, metadata.requests, "and IMDS is not consulted without a MAC")

	// it comes back with the same MAC, so the cache is good again
	readable = true
	eniID, err = resolver.ENIID(context.Background(), "ens5")
	require.NoError(t, err)
	assert.Equal(t, "eni-0123456789abcdef0", eniID)
	assert.Equal(t, 1, metadata.requests, "served from cache, still no new lookup")
}
