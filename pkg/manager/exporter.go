package manager

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

// Exporter handles propagating conditions from monitors to external systems
type Exporter interface {
	// Info exports informational conditions
	Info(ctx context.Context, condition monitor.Condition, conditionType corev1.NodeConditionType) error

	// Warning exports warning conditions
	Warning(ctx context.Context, condition monitor.Condition, conditionType corev1.NodeConditionType) error

	// Fatal exports fatal conditions
	Fatal(ctx context.Context, condition monitor.Condition, conditionType corev1.NodeConditionType) error

	// Resolve clears a previously exported fatal condition reason. It returns
	// true when the node condition has returned to a healthy status, i.e.
	// no other fatal reasons remain for it. Resolving a reason that was
	// never reported is a no-op.
	Resolve(ctx context.Context, condition monitor.Condition, conditionType corev1.NodeConditionType) (bool, error)
}
