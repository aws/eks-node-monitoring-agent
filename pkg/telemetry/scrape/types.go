package scrape

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

type Collector interface {
	Scope() string
	Scrape(ctx context.Context) Result
}

type Result struct {
	nodetelemetry.Scope
	Failure string
	Advance func()
}

const (
	ReasonConnectionRefused = "ConnectionRefused"
	ReasonTimeout           = "Timeout"
	ReasonResponseTooLarge  = "ResponseTooLarge"
	ReasonParseError        = "ParseError"
	ReasonIdentityMismatch  = "IdentityMismatch"
)
