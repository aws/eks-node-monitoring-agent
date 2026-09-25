package manager_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
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
	notifyChan   chan struct{}
	warningChan  chan monitor.Condition
	resolveChan  chan monitor.Condition
	resolveValue bool
}

func (e *mockExporter) notify() error {
	e.notifyChan <- struct{}{}
	return nil
}
func (e *mockExporter) Info(context.Context, monitor.Condition, corev1.NodeConditionType) error {
	return e.notify()
}
func (e *mockExporter) Warning(_ context.Context, condition monitor.Condition, _ corev1.NodeConditionType) error {
	if e.warningChan != nil {
		e.warningChan <- condition
		return nil
	}
	return e.notify()
}
func (e *mockExporter) Fatal(context.Context, monitor.Condition, corev1.NodeConditionType) error {
	return e.notify()
}
func (e *mockExporter) Resolve(_ context.Context, condition monitor.Condition, _ corev1.NodeConditionType) (bool, error) {
	if e.resolveChan != nil {
		e.resolveChan <- condition
	}
	return e.resolveValue, nil
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

func TestManager_ResolvedConditionRoutedToExporter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second)
	defer cancel()

	mockMon := &mockMonitor{
		registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
			go mgr.Notify(ctx, monitor.Condition{
				Reason:   "ExampleReason",
				Severity: monitor.SeverityFatal,
				Resolved: true,
				// MinOccurrences must not gate resolution.
				MinOccurrences: 5,
			})
			return nil
		},
	}
	mMgr, mockExp := NewManagerWithExporterFuncs(func(e *mockExporter) {
		e.resolveChan = make(chan monitor.Condition, 1)
		e.resolveValue = true
	})
	if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
		t.Fatal(err)
	}
	go mMgr.Start(ctx)

	select {
	case cond := <-mockExp.resolveChan:
		if cond.Reason != "ExampleReason" {
			t.Fatalf("resolved reason = %q, want ExampleReason", cond.Reason)
		}
	case <-ctx.Done():
		t.Fatal("resolved condition was not routed to the exporter")
	}
}

func TestManager_ResolvedConditionNotCountedAsProblem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second)
	defer cancel()

	const reason = "ResolvedNotCounted"
	mockMon := &mockMonitor{
		registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
			go mgr.Notify(ctx, monitor.Condition{Reason: reason, Severity: monitor.SeverityFatal, Resolved: true})
			return nil
		},
	}
	mMgr, mockExp := NewManagerWithExporterFuncs(func(e *mockExporter) {
		e.resolveChan = make(chan monitor.Condition, 1)
		e.resolveValue = true
	})
	if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
		t.Fatal(err)
	}
	go mMgr.Start(ctx)

	select {
	case <-mockExp.resolveChan:
	case <-ctx.Done():
		t.Fatal("resolved condition was not routed to the exporter")
	}
	if got := problemConditionCount(t, monitor.SeverityFatal, reason); got != 0 {
		t.Errorf("problem_condition_count{reason=%q} = %v after a resolve, want 0", reason, got)
	}
}

func TestManager_ResolvedNonFatalRoutedBySeverity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second)
	defer cancel()

	mockMon := &mockMonitor{
		registerFunc: func(ctx context.Context, mgr monitor.Manager) error {
			go mgr.Notify(ctx, monitor.Condition{Reason: "ExampleReason", Severity: monitor.SeverityWarning, Resolved: true})
			return nil
		},
	}
	mMgr, mockExp := NewManagerWithExporterFuncs(func(e *mockExporter) {
		e.warningChan = make(chan monitor.Condition, 1)
		e.resolveChan = make(chan monitor.Condition, 1)
	})
	if err := mMgr.Register(ctx, mockMon, "MockPassed"); err != nil {
		t.Fatal(err)
	}
	go mMgr.Start(ctx)

	select {
	case cond := <-mockExp.warningChan:
		if cond.Reason != "ExampleReason" {
			t.Fatalf("warning reason = %q, want ExampleReason", cond.Reason)
		}
	case <-mockExp.resolveChan:
		t.Fatal("a non-fatal condition must not take the resolve path")
	case <-ctx.Done():
		t.Fatal("resolved Warning was not routed to the exporter")
	}
}

// problemConditionCount reads problem_condition_count for one series from the
// registry the manager registers its metrics with. A series that was never
// incremented reads as 0.
func problemConditionCount(t *testing.T, severity monitor.Severity, reason string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "problem_condition_count" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["severity"] == string(severity) && labels["reason"] == reason {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
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
