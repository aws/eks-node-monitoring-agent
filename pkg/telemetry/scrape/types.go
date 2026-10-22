package scrape

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

// Collector collects one component's scope for a report.
type Collector interface {
	// Scope returns the component's report scope name.
	Scope() string
	// Scrape collects the component's scope for the current interval.
	Scrape(ctx context.Context) Result
}

// Result is the result of one Scrape.
type Result struct {
	nodetelemetry.Scope
	// Failure is the reason the scrape failed, or empty if it succeeded.
	Failure string
	// Advance, if set, advances the counter baselines once the report is
	// accepted or dropped.
	Advance func()
}

// Reasons a scrape fails, sent as eks_not_reporting reasons.
const (
	ReasonConnectionRefused = "ConnectionRefused"
	ReasonTimeout           = "Timeout"
	ReasonResponseTooLarge  = "ResponseTooLarge"
	ReasonParseError        = "ParseError"
	ReasonIdentityMismatch  = "IdentityMismatch"
)
