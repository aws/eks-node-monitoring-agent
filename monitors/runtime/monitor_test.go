package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
	"golang.a2z.com/Eks-node-monitoring-agent/test/fake"
)

var fakeClient = &fake.FakeKubeClient{}

func makeRuntimeMonitor() *runtimeMonitor {
	return NewRuntimeMonitor(&corev1.Node{}, fakeClient)
}

type mockManager struct {
	err error
	obs observer.BaseObserver
	res chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.res <- condition
	return nil
}

func TestRuntimeMonitor(t *testing.T) {
	for _, testCase := range []struct {
		log      string
		severity monitor.Severity
	}{
		// systemd logs
		{"systemd[1]: Unit kubelet.service entered failed state.", monitor.SeverityWarning},
		// kubelet logs
		{"Failed to start containerd.", monitor.SeverityWarning},
		{"OCI runtime create failed: unable to retrieve OCI runtime error", monitor.SeverityWarning},
		{`"Pod still has one or more containers in the non-exited state and will not be removed from desired state" pod="default/a-pod"`, monitor.SeverityFatal},
	} {
		t.Run(testCase.log, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			mon := makeRuntimeMonitor()
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
				assert.Equal(t, testCase.severity, monitorResult.Severity)
			}
		})
	}

	t.Run("SubscribeError", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := makeRuntimeMonitor()
		mockError := fmt.Errorf("mock error")
		mockManager := &mockManager{err: mockError, obs: observer.BaseObserver{}}
		actualError := mon.Register(ctx, mockManager)
		assert.EqualError(t, actualError, mockError.Error())
	})

	t.Run("ReadinessProbeFailures", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := makeRuntimeMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		for range 5 /* burst */ + 1 {
			mockManager.obs.Broadcast("mock", `Readiness probe for "foo:bar" failed`)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "ReadinessProbeFailures", monitorResult.Reason)
		}
	})

	t.Run("LivenessProbeFailures", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := makeRuntimeMonitor()
		mockManager := &mockManager{
			obs: observer.BaseObserver{},
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, mockManager)
		for range 5 /* burst */ + 1 {
			mockManager.obs.Broadcast("mock", `Liveness probe for "foo:bar" failed`)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "LivenessProbeFailures", monitorResult.Reason)
		}
	})

	for _, testCase := range []struct {
		message            string
		expectNotification bool
	}{
		{
			// NOTE: this timestamp uses a value from the future so that it is
			// always detected as a current issue!
			message: `
[
	{
		"id": "io.containerd.deprecation/mock",
		"message": "mock deprecation message",
		"lastOccurrence": "2222-01-01T00:00:00.000000000Z"
	}
]
`,
			expectNotification: true,
		},
		{
			message:            ``,
			expectNotification: false,
		},
	} {
		t.Run("ContainerdConfigDeprecations", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			mon := makeRuntimeMonitor()
			mockManager := &mockManager{
				obs: observer.BaseObserver{},
				res: make(chan monitor.Condition, 5),
			}
			mon.Register(ctx, mockManager)
			err := mon.checkContainerdWarnings([]byte(testCase.message))
			assert.NoError(t, err)
			select {
			case <-ctx.Done():
				if testCase.expectNotification {
					t.Fatal(ctx.Err())
				}
			case montiorResult := <-mockManager.res:
				if testCase.expectNotification {
					assert.Equal(t, monitor.SeverityWarning, montiorResult.Severity)
					assert.Equal(t, "DeprecatedContainerdConfiguration", montiorResult.Reason)
				} else {
					t.Fatalf("Got an unexpected notification: %v", montiorResult)
				}
			}
		})
	}

	t.Run("ManifestV2SchemaV1Annotation", func(t *testing.T) {
		// TODO: this block is temporary to enable code paths for EKS Auto
		runtimeContext := config.GetRuntimeContext()
		lastRuntimeContext := *runtimeContext
		runtimeContext.AddTags(config.EKSAuto)
		defer func() {
			// restore the original context
			*runtimeContext = lastRuntimeContext
		}()

		t.Run("DoesNotAddAnnotation", func(t *testing.T) {
			m := makeRuntimeMonitor()
			assert.NoError(t, m.reconcileManifestWarning(context.TODO(), []deprecationWarning{{
				ID:             "wrong-id",
				LastOccurrence: time.Now(),
			}}))
			assert.NotContains(t, m.node.Annotations, dockerManifestV2SchemaV1Annotation)
		})
		t.Run("AddsAnnotation", func(t *testing.T) {
			timestamp := time.Now()

			m := makeRuntimeMonitor()
			assert.NoError(t, m.reconcileManifestWarning(context.TODO(), []deprecationWarning{{
				ID:             "io.containerd.deprecation/pull-schema-1-image",
				LastOccurrence: timestamp,
			}}))
			assert.Equal(t, timestamp.Format(time.RFC3339Nano), m.node.Annotations[dockerManifestV2SchemaV1Annotation])
		})
		t.Run("RemovesAnnotation", func(t *testing.T) {
			m := makeRuntimeMonitor()
			m.node = &corev1.Node{ObjectMeta: v1.ObjectMeta{Annotations: map[string]string{
				"io.containerd.deprecation/pull-schema-1-image": time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano),
			}}}
			assert.NoError(t, m.reconcileManifestWarning(context.TODO(), []deprecationWarning{}))
			assert.NotContains(t, m.node.Annotations, dockerManifestV2SchemaV1Annotation)
		})
		t.Run("UpdatesAnnotation", func(t *testing.T) {
			afterTimestamp := time.Now()
			beforeTimestamp := afterTimestamp.Add(-2 * time.Hour)

			m := makeRuntimeMonitor()
			m.node = &corev1.Node{ObjectMeta: v1.ObjectMeta{Annotations: map[string]string{
				"io.containerd.deprecation/pull-schema-1-image": beforeTimestamp.Format(time.RFC3339Nano),
			}}}
			assert.NoError(t, m.reconcileManifestWarning(context.TODO(), []deprecationWarning{{
				ID:             "io.containerd.deprecation/pull-schema-1-image",
				LastOccurrence: afterTimestamp,
			}}))
			assert.Equal(t, afterTimestamp.Format(time.RFC3339Nano), m.node.Annotations[dockerManifestV2SchemaV1Annotation])
		})
	})
}

// TestUnitName tests the unitName helper function
func TestUnitName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"kubelet.service", "Kubelet"},
		{"containerd.service", "Containerd"},
		{"docker.service", "Docker"},
		{"my-custom-service.service", "My-Custom-Service"},
		{"already-formatted", "Already-Formatted"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := unitName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCheckContainerdWarnings_InvalidJSON tests error handling for malformed JSON
func TestCheckContainerdWarnings_InvalidJSON(t *testing.T) {
	mon := makeRuntimeMonitor()
	mockManager := &mockManager{
		obs: observer.BaseObserver{},
		res: make(chan monitor.Condition, 5),
	}
	mon.manager = mockManager

	invalidJSON := []byte(`{"invalid json`)
	err := mon.checkContainerdWarnings(invalidJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse containerd deprecation warnings")
}

// TestCheckContainerdWarnings_OldDeprecationsFiltered tests that old deprecations are filtered out
func TestCheckContainerdWarnings_OldDeprecationsFiltered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	mon := makeRuntimeMonitor()
	mockManager := &mockManager{
		obs: observer.BaseObserver{},
		res: make(chan monitor.Condition, 5),
	}
	mon.Register(ctx, mockManager)

	// Create a deprecation with an old timestamp (more than 1 hour ago)
	oldTimestamp := time.Now().Add(-2 * time.Hour)
	oldDeprecationJSON := fmt.Sprintf(`
[
	{
		"id": "io.containerd.deprecation/old-warning",
		"message": "This is an old warning",
		"lastOccurrence": "%s"
	}
]
`, oldTimestamp.Format(time.RFC3339Nano))

	err := mon.checkContainerdWarnings([]byte(oldDeprecationJSON))
	assert.NoError(t, err)

	// Should not receive any notification because the deprecation is too old
	select {
	case <-ctx.Done():
		// Expected - no notification should be sent
	case notification := <-mockManager.res:
		t.Fatalf("Expected no notification for old deprecation, but got: %v", notification)
	case <-time.After(100 * time.Millisecond):
		// Expected - no notification within timeout
	}
}

// TestNewPlugin tests the plugin factory function
func TestNewPlugin(t *testing.T) {
	node := &corev1.Node{ObjectMeta: v1.ObjectMeta{Name: "test-node"}}
	plugin := NewPlugin(node, fakeClient)

	assert.NotNil(t, plugin)
	assert.Equal(t, "runtime", plugin.Name())

	monitors := plugin.Monitors()
	assert.Len(t, monitors, 1)
	assert.Equal(t, "container-runtime", monitors[0].Name())
}
