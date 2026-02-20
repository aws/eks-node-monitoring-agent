package nccl

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func TestNCCL(t *testing.T) {
	kmsgChan := make(chan string, 1)
	ncclSystem := NewNCCLSystem(kmsgChan)
	kmsgChan <- "segfault at 7f797fe00000 ip 00007f7c7ac69996 sp 00007f7c12fd7c30 error 4 in libnccl.so.2[7f7c7ac00000+d3d3000]"
	conditions, err := ncclSystem.Step(context.TODO())
	assert.NoError(t, err)
	assert.NotEmpty(t, conditions)
	assert.Equal(t, conditions[0], monitor.Condition{
		Reason:   "NvidiaNCCLError",
		Message:  "NCCL communication error caused by segfault in libnccl.so",
		Severity: monitor.SeverityWarning,
	})
}
