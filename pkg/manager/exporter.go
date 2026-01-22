package manager

import (
	"context"

	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
)

// Exporter handles propagating conditions from monitors to external systems
type Exporter interface {
	// Info exports informational conditions
	Info(ctx context.Context, condition monitor.Condition, source string) error

	// Warning exports warning conditions
	Warning(ctx context.Context, condition monitor.Condition, source string) error

	// Fatal exports fatal conditions
	Fatal(ctx context.Context, condition monitor.Condition, source string) error
}
