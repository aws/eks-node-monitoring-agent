package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/pkg/manager"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

type fatals []manager.ActiveFatalCondition

func (f fatals) ActiveFatalConditions() []manager.ActiveFatalCondition { return f }

func TestInProcessCollector(t *testing.T) {
	c := NewInProcessCollector(fatals{{ConditionType: "StorageReady", Reason: "IOError"}}, map[string]string{"instanceId": "i-1"})
	res := c.Scrape(context.Background())
	assert.Equal(t, NMAScope, res.Scope.Name)
	assert.Equal(t, []nodetelemetry.Problem{{Type: "StorageReady", Reason: "IOError", Resources: map[string]string{"instanceId": "i-1"}}}, res.Problems)
}
