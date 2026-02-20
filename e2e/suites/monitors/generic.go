package monitors

import (
	"context"
	"testing"
	"time"

	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func Generic() types.Feature {
	var targetNode *corev1.Node

	return features.New("Generic").
		WithLabel("type", "generic").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			targetNode = &nodeList.Items[0]
			t.Logf("targetting node %q for test", targetNode.Name)
			return ctx
		}).
		AssessWithDescription("ManagedConditions", "heartbeats should replace repair-managed node conditions on the api server with the agent's in-memory records",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// pick any arbitrary condition that is managed/heartbeat by the agent
				const managedCondition = nodeconditions.NetworkingReady
				for i, condition := range targetNode.Status.Conditions {
					if condition.Type == managedCondition {
						if targetNode.Status.Conditions[i].Status == corev1.ConditionFalse {
							t.Fatalf("node condition %q is already set to %q", managedCondition, corev1.ConditionFalse)
						}
						targetNode.Status.Conditions[i].Status = corev1.ConditionFalse
					}
				}
				if err := cfg.Client().Resources().UpdateStatus(ctx, targetNode); err != nil {
					t.Fatalf("overwriting node condition %q: %s", managedCondition, err)
				}
				statusesRecovered := func(o k8s.Object) bool {
					node := o.(*corev1.Node)
					for _, condition := range node.Status.Conditions {
						if condition.Type == managedCondition && condition.Status == corev1.ConditionTrue {
							return true
						}
					}
					return false
				}
				if err := wait.For(
					conditions.New(cfg.Client().Resources()).ResourceMatch(targetNode, statusesRecovered),
					// the hearbeat is a 5 minute period, so give a little bit
					// of extra leeway to help with the interval.
					wait.WithTimeout(6*time.Minute),
				); err != nil {
					t.Fatalf("waiting for node condition %q to flip back to %q: %s", managedCondition, corev1.ConditionTrue, err)
				}
				return ctx
			}).
		Feature()
}
