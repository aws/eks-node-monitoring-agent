package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// reportInterval is the interval at which the local state is applied to the node.
	// This ensures that changes to multiple managed conditions within this time period are reported in a single API call.
	reportInterval = 15 * time.Second

	// heartbeatInterval is the interval at which managed condition heartbeat times are updated.
	heartbeatInterval = 5 * time.Minute
)

var _ Exporter = (*nodeExporter)(nil)

// NodeConditionConfig holds the ready state configuration for a node condition
type NodeConditionConfig struct {
	ReadyReason  string
	ReadyMessage string
}

// NewNodeExporter creates a new node exporter that updates Kubernetes node conditions
func NewNodeExporter(
	node *corev1.Node,
	kubeClient client.Client,
	recorder record.EventRecorder,
	managedConditionConfigs map[corev1.NodeConditionType]NodeConditionConfig,
) *nodeExporter {
	return &nodeExporter{
		nodeRef:                makeNodeReference(node),
		nodeKey:                client.ObjectKeyFromObject(node),
		kubeClient:             kubeClient,
		recorder:               recorder,
		conditionConfigs:       managedConditionConfigs,
		managedConditions:      initializeManagedConditions(managedConditionConfigs),
		expiries:               make(map[corev1.NodeConditionType]map[string]time.Time),
		managedConditionsDirty: true,
	}
}

// makeNodeReference returns an ObjectReference for the specified node that can be (re)used for event recordings.
func makeNodeReference(node *corev1.Node) *corev1.ObjectReference {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(fmt.Errorf("failed to add core v1 types to scheme: %v", err))
	}
	ref, err := reference.GetReference(scheme, node)
	if err != nil {
		panic(fmt.Errorf("failed to construct node reference: %v", err))
	}
	// remove the resource version, it's not useful to us
	ref.ResourceVersion = ""
	return ref
}

func initializeManagedConditions(conditionConfigs map[corev1.NodeConditionType]NodeConditionConfig) map[corev1.NodeConditionType]corev1.NodeCondition {
	managedConditions := make(map[corev1.NodeConditionType]corev1.NodeCondition)
	now := metav1.Now()
	for conditionType, conditionConfig := range conditionConfigs {
		managedConditions[conditionType] = corev1.NodeCondition{
			Type:               conditionType,
			Status:             corev1.ConditionTrue,
			Reason:             conditionConfig.ReadyReason,
			Message:            conditionConfig.ReadyMessage,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
		}
	}
	return managedConditions
}

// nodeExporter implements monitor.Exporter by exposing conditions onto the k8s node resource
type nodeExporter struct {
	kubeClient client.Client
	recorder   record.EventRecorder
	nodeRef    *corev1.ObjectReference
	nodeKey    client.ObjectKey

	conditionConfigs       map[corev1.NodeConditionType]NodeConditionConfig
	managedConditions      map[corev1.NodeConditionType]corev1.NodeCondition
	managedConditionsDirty bool
	managedConditionsLock  sync.Mutex

	// expiries tracks, per condition type, the deadline by which each contributing
	// reason must be re-observed to stay latched. Reasons reported without a TTL
	// are absent here and therefore never expire. Keyed by reason so that a
	// condition aggregating several failures only clears once all of them lapse.
	expiries map[corev1.NodeConditionType]map[string]time.Time
}

// Info records an event for the specified condition.
func (e *nodeExporter) Info(ctx context.Context, c monitor.Condition, conditionType corev1.NodeConditionType) error {
	e.recorder.Event(e.nodeRef, corev1.EventTypeNormal, string(conditionType), fmt.Sprintf("%s: %s", c.Reason, c.Message))
	return nil
}

// Warning records an event for the specified condition.
func (e *nodeExporter) Warning(ctx context.Context, c monitor.Condition, conditionType corev1.NodeConditionType) error {
	e.recorder.Event(e.nodeRef, corev1.EventTypeWarning, string(conditionType), fmt.Sprintf("%s: %s", c.Reason, c.Message))
	return nil
}

// Fatal updates the local state for the specified managed condition.
// The condition will be reported in the Node.Status.Conditions periodically.
func (e *nodeExporter) Fatal(ctx context.Context, monitorCondition monitor.Condition, conditionType corev1.NodeConditionType) error {
	e.managedConditionsLock.Lock()
	defer e.managedConditionsLock.Unlock()
	now := metav1.Now()
	newCondition := corev1.NodeCondition{
		Type:               conditionType,
		Reason:             monitorCondition.Reason,
		Message:            monitorCondition.Message,
		Status:             corev1.ConditionFalse,
		LastTransitionTime: now,
		LastHeartbeatTime:  now,
	}
	if monitorCondition.TTL > 0 {
		if e.expiries[conditionType] == nil {
			e.expiries[conditionType] = make(map[string]time.Time)
		}
		e.expiries[conditionType][monitorCondition.Reason] = now.Add(monitorCondition.TTL)
	} else {
		// a reason reported without a TTL latches the condition, so drop any
		// expiry previously recorded for it.
		delete(e.expiries[conditionType], monitorCondition.Reason)
	}
	if oldCondition, ok := e.managedConditions[conditionType]; ok {
		// if the status has not changed, use the old transition time
		if oldCondition.Status == newCondition.Status {
			newCondition.LastTransitionTime = oldCondition.LastTransitionTime

			// aggregate messages if the status is the same (e.g. both False)
			// and ensure that we don't duplicate identical messages.
			if oldCondition.Message != "" && oldCondition.Message != newCondition.Message && !strings.Contains(oldCondition.Message, newCondition.Message) {
				newCondition.Message = oldCondition.Message + "; " + newCondition.Message
			} else if strings.Contains(oldCondition.Message, newCondition.Message) {
				// if the old message already contains the new one, preserve the old one (which might have other aggregated messages)
				newCondition.Message = oldCondition.Message
			}
		}
	}
	e.managedConditions[conditionType] = newCondition
	e.managedConditionsDirty = true
	return nil
}

// Run starts the node exporter's background tasks
func (e *nodeExporter) Run(ctx context.Context) {
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()
	reportTicker := time.NewTicker(reportInterval)
	defer reportTicker.Stop()
	e.RunWithTickers(ctx, heartbeatTicker.C, reportTicker.C)
}

// RunWithTickers is a long-running loop that wakes up for heartbeat or report ticks, and terminates when the context is done.
// The ticker channels are exposed directly for testing.
func (e *nodeExporter) RunWithTickers(ctx context.Context, heartbeatTicker <-chan time.Time, reportTicker <-chan time.Time) {
	log.FromContext(ctx).Info("starting node exporter")
	for {
		select {
		case <-heartbeatTicker:
			e.updateHeartbeatTimes()
		case <-reportTicker:
			e.expireConditions(ctx)
			if err := e.reportManagedConditions(ctx); err != nil {
				log.FromContext(ctx).Error(err, "failed to report managed conditions")
			}
		case <-ctx.Done():
			return
		}
	}
}

// expireConditions returns any Fatal condition whose contributing reasons have all
// passed their TTL back to its configured ready state. A condition with at least one
// un-expired or non-expiring reason is left alone.
func (e *nodeExporter) expireConditions(ctx context.Context) {
	e.managedConditionsLock.Lock()
	defer e.managedConditionsLock.Unlock()
	now := metav1.Now()
	for conditionType, reasonExpiries := range e.expiries {
		condition, ok := e.managedConditions[conditionType]
		if !ok || condition.Status != corev1.ConditionFalse {
			continue
		}
		// only clear when every reason holding this condition down has lapsed,
		// otherwise a still-failing reason would be silently dropped.
		for reason, expiry := range reasonExpiries {
			if now.Time.Before(expiry) {
				continue
			}
			delete(reasonExpiries, reason)
		}
		if len(reasonExpiries) > 0 {
			continue
		}
		conditionConfig, ok := e.conditionConfigs[conditionType]
		if !ok {
			// without a ready reason/message there is nothing sane to revert to.
			continue
		}
		delete(e.expiries, conditionType)
		log.FromContext(ctx).Info("clearing expired condition",
			"conditionType", conditionType,
			"previousReason", condition.Reason,
		)
		e.recorder.Event(e.nodeRef, corev1.EventTypeNormal, string(conditionType),
			fmt.Sprintf("%s: condition cleared, %s was not re-observed", conditionConfig.ReadyReason, condition.Reason))
		e.managedConditions[conditionType] = corev1.NodeCondition{
			Type:               conditionType,
			Reason:             conditionConfig.ReadyReason,
			Message:            conditionConfig.ReadyMessage,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			LastHeartbeatTime:  now,
		}
		e.managedConditionsDirty = true
	}
}

// updateHeartbeatTimes sets the managed condition heartbeat times to the current time, and marks the local state as dirty.
// This causes all managed conditions to be reported the next time reportManagedConditions is called.
func (e *nodeExporter) updateHeartbeatTimes() {
	e.managedConditionsLock.Lock()
	defer e.managedConditionsLock.Unlock()
	now := metav1.Now()
	for condType := range e.managedConditions {
		cond := e.managedConditions[condType]
		cond.LastHeartbeatTime = now
		e.managedConditions[condType] = cond
	}
	e.managedConditionsDirty = true
}

// reportManagedConditions applies the managed conditions to the node with an "upsert" strategy.
// If the local state is not dirty, this is a no-op.
// If a managed condition does not exist, or has been removed by another API client, it will be appended to the node's conditions.
// If a managed condition already exists, it will be replaced by our local copy.
func (e *nodeExporter) reportManagedConditions(ctx context.Context) error {
	e.managedConditionsLock.Lock()
	defer e.managedConditionsLock.Unlock()
	if !e.managedConditionsDirty {
		return nil
	}
	log.FromContext(ctx).Info("reporting managed conditions")
	var oldNode corev1.Node
	if err := e.kubeClient.Get(ctx, e.nodeKey, &oldNode); err != nil {
		return err
	}
	newNode := oldNode.DeepCopy()
	conditions := newNode.Status.Conditions
	for _, managedCondition := range e.managedConditions {
		found := false
		for i, condition := range conditions {
			if managedCondition.Type == condition.Type {
				newNode.Status.Conditions[i] = managedCondition
				found = true
				break
			}
		}
		if !found {
			newNode.Status.Conditions = append(newNode.Status.Conditions, managedCondition)
		}
	}
	if err := e.kubeClient.Status().Patch(ctx, newNode, client.MergeFrom(&oldNode)); err != nil {
		return err
	}
	e.managedConditionsDirty = false
	log.FromContext(ctx).Info("reported node conditions")
	return nil
}
