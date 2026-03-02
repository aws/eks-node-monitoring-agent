package config_test

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestRuntimeContext(t *testing.T) {
	var runtimeContext *config.RuntimeContext
	assert.NotPanics(t, func() { runtimeContext = config.GetRuntimeContext() })

	runtimeContext.AcceleratedHardware()
	runtimeContext.OSDistro()

	runtimeContext.AddTags("foo")
	assert.Contains(t, runtimeContext.Tags(), "foo")
}
