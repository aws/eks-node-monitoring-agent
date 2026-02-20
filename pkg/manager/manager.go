package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

var (
	conditionCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "problem_condition_count"},
		[]string{"severity", "reason"},
	)
	conditionTypeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "fatal_condition_gauge"},
		[]string{"type"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		conditionCount,
		conditionTypeGauge,
	)
}

// MonitorManager manages the lifecycle of monitors and routes their notifications
type MonitorManager struct {
	nodeName          string
	monitors          map[string]monitor.Monitor
	conditionTypeMap  map[string]corev1.NodeConditionType
	conditionCountMap map[string]int64
	observers         map[string]observer.Observer
	notifyChan        chan notification
	exporter          Exporter
}

type notification struct {
	monitorName string
	condition   monitor.Condition
}

// NewMonitorManager creates a new monitor manager
func NewMonitorManager(nodeName string, exporter Exporter) *MonitorManager {
	return &MonitorManager{
		nodeName:          nodeName,
		monitors:          make(map[string]monitor.Monitor),
		conditionTypeMap:  make(map[string]corev1.NodeConditionType),
		conditionCountMap: make(map[string]int64),
		observers:         make(map[string]observer.Observer),
		notifyChan:        make(chan notification, 100),
		exporter:          exporter,
	}
}

// Register registers a monitor with the manager
func (m *MonitorManager) Register(ctx context.Context, mon monitor.Monitor, conditionType corev1.NodeConditionType) error {
	m.monitors[mon.Name()] = mon
	m.conditionTypeMap[mon.Name()] = conditionType
	return mon.Register(ctx, makeManagerWrapper(m, mon))
}

// Start starts all observers and begins processing notifications
func (m *MonitorManager) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Start all observers
	for id, obs := range m.observers {
		obsLogger := logger.WithValues("observer", id)
		obsLogger.Info("starting observer")
		go func(o observer.Observer, l logr.Logger) {
			obsCtx := log.IntoContext(ctx, l)
			if err := o.Init(obsCtx); err != nil {
				l.Error(err, "observer failed")
			}
		}(obs, obsLogger)
	}

	// Process notifications
	return m.runLoop(ctx)
}

func (m *MonitorManager) runLoop(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Poll ticker for periodic condition checks
	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicker.C:
			// Poll monitors for their current conditions
			for _, mon := range m.monitors {
				for _, cond := range mon.Conditions() {
					if err := m.exportCondition(ctx, mon.Name(), cond); err != nil {
						logger.Error(err, "failed to export condition", "source", mon.Name(), "condition", cond)
					}
				}
			}
		case notif := <-m.notifyChan:
			if err := m.exportCondition(ctx, notif.monitorName, notif.condition); err != nil {
				logger.Error(err, "failed to export condition",
					"monitor", notif.monitorName,
					"condition", notif.condition,
				)
			}
		}
	}
}

func (m *MonitorManager) exportCondition(ctx context.Context, monitorName string, condition monitor.Condition) error {
	logger := log.FromContext(ctx).WithValues("source", monitorName, "condition", condition)

	// track condition metrics
	conditionCount.WithLabelValues(string(condition.Severity), condition.Reason).Add(1)

	conditionType, ok := m.conditionTypeMap[monitorName]
	if !ok {
		return fmt.Errorf("missing condition type mapping for monitor: %s", monitorName)
	}
	logger = logger.WithValues("conditionType", conditionType)

	// Skip requests for conditions that have not met their minimum occurrences
	if m.conditionCountMap[condition.Reason] < condition.MinOccurrences {
		logger.Info("condition has not met MinOccurrences", "occurrences", m.conditionCountMap[condition.Reason])
		m.conditionCountMap[condition.Reason] += 1
		return nil
	}
	m.conditionCountMap[condition.Reason] = 0

	return m.SendCondition(ctx, condition, conditionType)
}

// SendCondition sends a condition to the exporter based on severity
func (m *MonitorManager) SendCondition(ctx context.Context, condition monitor.Condition, conditionType corev1.NodeConditionType) error {
	log.FromContext(ctx).Info("sending condition to exporter", "condition", condition, "conditionType", conditionType)
	switch condition.Severity {
	case monitor.SeverityInfo:
		return m.exporter.Info(ctx, condition, conditionType)
	case monitor.SeverityWarning:
		return m.exporter.Warning(ctx, condition, conditionType)
	case monitor.SeverityFatal:
		for _, cType := range m.conditionTypeMap {
			if conditionType == cType {
				conditionTypeGauge.WithLabelValues(string(cType)).Set(1.0)
			}
		}
		return m.exporter.Fatal(ctx, condition, conditionType)
	default:
		return fmt.Errorf("invalid condition severity: %q", condition.Severity)
	}
}

// Subscribe implements the monitor.Manager interface for resource subscriptions
func (m *MonitorManager) Subscribe(rType resource.Type, rParts []resource.Part) (<-chan string, error) {
	rID := resourceID(rType, rParts)
	obs, ok := m.observers[rID]
	if !ok {
		constructor, ok := observer.ObserverConstructorMap[rType]
		if !ok {
			return nil, fmt.Errorf("the resource type %q was not handled", rType)
		}
		var err error
		if obs, err = constructor(rParts); err != nil {
			return nil, err
		}
		m.observers[rID] = obs
	}
	return obs.Subscribe(), nil
}

// makeManagerWrapper creates a wrapper that implements monitor.Manager for a specific monitor
func makeManagerWrapper(monMgr *MonitorManager, mon monitor.Monitor) *managerWrapper {
	return &managerWrapper{
		MonitorManager: monMgr,
		notifyFunc: func(ctx context.Context, condition monitor.Condition) error {
			notif := notification{
				monitorName: mon.Name(),
				condition:   condition,
			}
			select {
			case monMgr.notifyChan <- notif:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

var _ monitor.Manager = (*managerWrapper)(nil)

// managerWrapper implements the Manager interface from the monitoring API
// package which scopes the notify call to the manager.
type managerWrapper struct {
	*MonitorManager
	notifyFunc func(ctx context.Context, condition monitor.Condition) error
}

func (m *managerWrapper) Notify(ctx context.Context, cond monitor.Condition) error {
	return m.notifyFunc(ctx, cond)
}

// resourceID creates a unique identifier for a resource subscription
func resourceID(rType resource.Type, rParts []resource.Part) string {
	id := string(rType)
	for _, part := range rParts {
		id += "-" + string(part)
	}
	return id
}
