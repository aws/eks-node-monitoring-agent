package insights

import (
	"bytes"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

const testMonitor = "test-monitor"

var linkLocal = monitor.Insight{
	Type:         "NetworkAllowanceExceeded",
	Reason:       "LinkLocal",
	ResourceName: "eni_id",
}

// exposition gathers the active insights as the text exposition format.
func exposition(t *testing.T, active *Active) string {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(active))
	families, err := registry.Gather()
	require.NoError(t, err)
	var buf bytes.Buffer
	for _, family := range families {
		require.NoError(t, expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain)).Encode(family))
	}
	return buf.String()
}

func TestActive_Replace(t *testing.T) {
	active := New()
	assert.Empty(t, exposition(t, active), "no insights, no series")

	active.Replace(testMonitor, linkLocal, []string{"eni-1", "eni-2"})
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="LinkLocal",resource_eni_id="eni-1",type="NetworkAllowanceExceeded"} 1
eks_insight{reason="LinkLocal",resource_eni_id="eni-2",type="NetworkAllowanceExceeded"} 1
`, exposition(t, active), "one series per resource")

	active.Replace(testMonitor, linkLocal, []string{"eni-2"})
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="LinkLocal",resource_eni_id="eni-2",type="NetworkAllowanceExceeded"} 1
`, exposition(t, active), "a resource left out has its series removed, not set to 0")

	active.Replace(testMonitor, linkLocal, nil)
	assert.Empty(t, exposition(t, active), "an empty set clears them all")
}

func TestActive_Replace_RepeatedResource(t *testing.T) {
	active := New()
	active.Replace(testMonitor, linkLocal, []string{"eni-1", "eni-1"})
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="LinkLocal",resource_eni_id="eni-1",type="NetworkAllowanceExceeded"} 1
`, exposition(t, active), "a resource reported twice is one series, not a duplicate the registry rejects")
}

func TestActive_Replace_OtherInsightsUntouched(t *testing.T) {
	active := New()
	active.Replace(testMonitor, linkLocal, []string{"eni-1"})
	active.Replace(testMonitor, monitor.Insight{Type: "StorageReady", Reason: "IOError", ResourceName: "volume_id"}, []string{"vol-1"})
	active.Replace(testMonitor, monitor.Insight{Type: "KernelReady", Reason: "Panic"}, []string{""})
	// another reason of the same type, and another type, are both left alone
	active.Replace(testMonitor, monitor.Insight{Type: linkLocal.Type, Reason: "BandwidthOut", ResourceName: linkLocal.ResourceName}, nil)
	active.Replace(testMonitor, monitor.Insight{Type: "StorageReady", Reason: "DeviceRemoved", ResourceName: "volume_id"}, nil)

	published := exposition(t, active)
	assert.Contains(t, published, `eks_insight{reason="LinkLocal",resource_eni_id="eni-1",type="NetworkAllowanceExceeded"} 1`)
	assert.Contains(t, published, `eks_insight{reason="IOError",resource_volume_id="vol-1",type="StorageReady"} 1`,
		"insights about different resources are published together")
	assert.Contains(t, published, `eks_insight{reason="Panic",type="KernelReady"} 1`,
		"an insight about no particular resource carries no resource label")
}

func TestActive_Replace_NoResource(t *testing.T) {
	active := New()
	// several IDs in a group that names no resource: without a resource label
	// every one of them is the same series, and a registry fails the whole
	// scrape on a duplicate, taking every other metric down with it
	active.Replace(testMonitor, monitor.Insight{Type: "KernelReady", Reason: "Panic"}, []string{"", "ignored", "also-ignored"})
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="Panic",type="KernelReady"} 1
`, exposition(t, active), "an insight about no resource is one series, not one per ID")

	active.Replace(testMonitor, monitor.Insight{Type: "KernelReady", Reason: "Panic"}, nil)
	assert.Empty(t, exposition(t, active), "and it still clears")
}

func TestActive_Replace_ScopedPerMonitor(t *testing.T) {
	active := New()
	active.Replace("monitor-a", linkLocal, []string{"eni-1"})
	active.Replace("monitor-b", linkLocal, []string{"eni-2"})

	// one monitor reporting its own set must not clear the other's, even
	// though both report the same kind of insight
	active.Replace("monitor-a", linkLocal, nil)
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="LinkLocal",resource_eni_id="eni-2",type="NetworkAllowanceExceeded"} 1
`, exposition(t, active), "clearing one monitor's set leaves the other's alone")
}

func TestActive_Collect_SameResourceFromTwoMonitors(t *testing.T) {
	active := New()
	// both monitors naming the same resource would be the same series twice,
	// which a registry rejects by failing the whole scrape
	active.Replace("monitor-a", linkLocal, []string{"eni-1"})
	active.Replace("monitor-b", linkLocal, []string{"eni-1"})
	assert.Equal(t, `# HELP eks_insight An insight was detected. Only present while the insight is active.
# TYPE eks_insight gauge
eks_insight{reason="LinkLocal",resource_eni_id="eni-1",type="NetworkAllowanceExceeded"} 1
`, exposition(t, active), "the duplicate is sent once")

	active.Replace("monitor-a", linkLocal, nil)
	assert.Contains(t, exposition(t, active), `resource_eni_id="eni-1"`,
		"and it stays while the other monitor still reports it")
}
