package manager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeEventRecorder struct {
	record.EventRecorder

	events corev1.EventList
}

func (r *fakeEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	r.events.Items = append(r.events.Items, corev1.Event{
		Type:    eventtype,
		Reason:  reason,
		Message: message,
	})
}

func TestNodeExporter_EventsRecordedImmediately(t *testing.T) {
	ctx := context.TODO()

	fakeClient := fake.NewFakeClient()
	nodeName := "test-node"
	initialNode := corev1.Node{
		ObjectMeta: v1.ObjectMeta{Name: nodeName},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Reason:             "Ready",
					Status:             corev1.ConditionTrue,
					Message:            "Hello, world",
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	if err := fakeClient.Create(ctx, &initialNode); err != nil {
		t.Fatalf("failed to create initial node: %v", err)
	}

	var recorder fakeEventRecorder

	nodeExporter := manager.NewNodeExporter(
		&initialNode,
		fakeClient,
		&recorder,
		map[corev1.NodeConditionType]manager.NodeConditionConfig{
			corev1.NodeReady: {
				ReadyReason:  "Ready",
				ReadyMessage: "Test Ready",
			},
		},
	)

	testConditionType := corev1.NodeConditionType("Test")
	testCondition := monitor.Condition{
		Reason:  "TestReason",
		Message: "TestMessage",
	}
	if err := nodeExporter.Info(ctx, testCondition, testConditionType); err != nil {
		t.Fatal(err)
	}
	if err := nodeExporter.Warning(ctx, testCondition, testConditionType); err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{corev1.EventTypeNormal, corev1.EventTypeWarning} {
		var found bool
		for _, event := range recorder.events.Items {
			if event.Reason == string(testConditionType) &&
				event.Message == fmt.Sprintf("%s: %s", testCondition.Reason, testCondition.Message) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("failed to verify event %s - %+v was present: %+v", eventType, testCondition, recorder.events)
		}
	}
}

func TestNodeExporter_ConditionReportedAfterTick(t *testing.T) {
	ctx := context.TODO()

	fakeClient := fake.NewFakeClient()
	nodeName := "test-node"
	initialNode := corev1.Node{
		ObjectMeta: v1.ObjectMeta{Name: nodeName},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Reason:             "Ready",
					Status:             corev1.ConditionTrue,
					Message:            "Hello, world",
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	if err := fakeClient.Create(ctx, &initialNode); err != nil {
		t.Fatalf("failed to create initial node: %v", err)
	}

	var recorder fakeEventRecorder

	nodeExporter := manager.NewNodeExporter(
		&initialNode,
		fakeClient,
		&recorder,
		map[corev1.NodeConditionType]manager.NodeConditionConfig{
			corev1.NodeReady: {
				ReadyReason:  "Ready",
				ReadyMessage: "Test Ready",
			},
		},
	)

	heartbeatChan := make(chan time.Time)
	reportChan := make(chan time.Time)

	go nodeExporter.RunWithTickers(ctx, heartbeatChan, reportChan)

	monitorCondition := monitor.Condition{
		Reason:  "TestReason",
		Message: "TestMessage",
	}
	conditionType := corev1.NodeConditionType("TestType")
	expectedCondition := corev1.NodeCondition{
		Type:    conditionType,
		Reason:  monitorCondition.Reason,
		Message: monitorCondition.Message,
		Status:  corev1.ConditionFalse,
	}

	if err := nodeExporter.Fatal(ctx, monitorCondition, conditionType); err != nil {
		t.Fatal(err)
	}

	nodeKey := client.ObjectKeyFromObject(&initialNode)

	var node corev1.Node
	if err := fakeClient.Get(ctx, nodeKey, &node); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	if nodeHasCondition(node, expectedCondition) {
		t.Fatalf("node condition updated before report tick")
	}

	reportChan <- time.Now()

	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 10*time.Second, true, func(ctx context.Context) (done bool, err error) {
		if err := fakeClient.Get(ctx, nodeKey, &node); err != nil {
			return false, fmt.Errorf("failed to get node: %v", err)
		}
		if !nodeHasCondition(node, expectedCondition) {
			t.Logf("node condition not found: %+v: %+v", expectedCondition, node.Status.Conditions)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("failed to verify node condition: %v", err)
	}
}

func nodeHasCondition(node corev1.Node, condition corev1.NodeCondition) bool {
	for _, c := range node.Status.Conditions {
		if isConditionEqual(c, condition) {
			return true
		}
	}
	return false
}

func isConditionEqual(l corev1.NodeCondition, r corev1.NodeCondition) bool {
	// ignore timestamps
	return l.Type == r.Type &&
		l.Status == r.Status &&
		l.Message == r.Message &&
		l.Reason == r.Reason
}
