package manager_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/manager"
)

type mockMonitor struct {
	registerFunc func(ctx context.Context, mgr monitor.Manager) error
}

func (m *mockMonitor) Name() string                    { return "mock" }
func (m *mockMonitor) Conditions() []monitor.Condition { return []monitor.Condition{} }
func (m *mockMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	return m.registerFunc(ctx, mgr)
}

func NewManagerWithExporterFuncs(fns ...func(*mockExporter)) (*manager.MonitorManager, *mockExporter) {
	mockExp := &mockExporter{notifyChan: make(chan struct{})}
	for _, fn := range fns {
		fn(mockExp)
	}
	mockManager := manager.NewMonitorManager("mock", mockExp)
	return mockManager, mockExp
}

type mockExporter struct {
	notifyChan chan struct{}
}

func (e *mockExporter) notify() error {
	e.notifyChan <- struct{}{}
	return nil
}
func (e *mockExporter) Info(context.Context, monitor.Condition, corev1.NodeConditionType) error {
	return e.notify()
}
func (e *mockExporter) Warning(context.Context, monitor.Condition, corev1.NodeConditionType) error {
	return e.notify()
}
func (e *mockExporter) Fatal(context.Context, monitor.Condition, corev1.NodeConditionType) error {
	return e.notify()
}

func TestManager_Notification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Millisecond)
	defer cancel()

	mockMon := &mockMonitor{
		registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
			go mgr.Notify(ctx, monitor.Condition{
				Reason:   "ExampleReason",
				Severity: monitor.SeverityFatal,
			})
			return nil
		},
	}
	mMgr, mockExp := NewManagerWithExporterFuncs()
	if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
		t.Fatal(err)
	}
	go mMgr.Start(ctx)

	select {
	case <-mockExp.notifyChan:
		// Notification was received by the exporter — the manager correctly
		// routed the condition from the monitor through to the exporter.
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestManager_MinOccurrences(t *testing.T) {
	t.Run("Met", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.TODO(), time.Millisecond)
		defer cancel()

		mockMon := &mockMonitor{
			registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
				go mgr.Notify(ctx, monitor.Condition{
					Reason:         "ExampleReason",
					Severity:       monitor.SeverityFatal,
					MinOccurrences: 0,
				})
				return nil
			},
		}
		mMgr, mockExp := NewManagerWithExporterFuncs()
		if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
			t.Fatal(err)
		}
		go mMgr.Start(ctx)

		select {
		case <-mockExp.notifyChan:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})

	t.Run("NotMet", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.TODO(), time.Millisecond)
		defer cancel()

		mockMon := &mockMonitor{
			registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
				go mgr.Notify(ctx, monitor.Condition{
					Reason:         "ExampleReason",
					Severity:       monitor.SeverityFatal,
					MinOccurrences: 2,
				})
				return nil
			},
		}
		mMgr, mockExp := NewManagerWithExporterFuncs()
		if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
			t.Fatal(err)
		}
		go mMgr.Start(ctx)

		select {
		case n := <-mockExp.notifyChan:
			t.Fatalf("expected no events on channel but got %+v", n)
		case <-ctx.Done():
			// expected to timeout because min occurrences was not met.
		}
	})
}

// this tests the creation of an observable resource from end to end.
func TestManager_CreateObserver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mMgr, _ := NewManagerWithExporterFuncs()
	_, err := mMgr.Subscribe(resource.ResourceTypeFile, []resource.Part{"/tmp/foobar"})
	assert.NoError(t, err)
	assert.NoError(t, mMgr.Start(ctx))
}
