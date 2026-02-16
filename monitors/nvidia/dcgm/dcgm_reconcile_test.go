//go:build !darwin

package dcgm_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func TestReconcile(t *testing.T) {
	t.Run("ReconcileError", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{ReconcileErr: fmt.Errorf("error")}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.Reconcile(context.TODO())
		// the error causes the condition to be emitted.
		assert.NoError(t, err)
		assert.NotEmpty(t, conditions)
		assert.Equal(t, conditions[0], monitor.Condition{
			Reason:   "DCGMError",
			Message:  mockDcgm.ReconcileErr.Error(),
			Severity: monitor.SeverityFatal,
		})
	})

	t.Run("ReconcileJustInit", func(t *testing.T) {
		mockDcgm := &fake.FakeDcgm{ReconcileJustInit: true}
		dcgmSystem := dcgm.NewDCGMSystem(mockDcgm, dcgm.GetDiagType())
		conditions, err := dcgmSystem.Reconcile(context.TODO())
		assert.NoError(t, err)
		assert.Empty(t, conditions)
	})
}
