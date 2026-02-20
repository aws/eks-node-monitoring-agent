//go:build !darwin

package fake_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/dcgm/fake"
)

func Test(t *testing.T) {
	dcgm := fake.FakeDcgm{
		PolicyChan:        make(chan dcgmapi.PolicyViolation),
		ReconcileErr:      fmt.Errorf("reconcile error"),
		ReconcileJustInit: true,
		DiagResults:       dcgmapi.DiagResults{Software: []dcgmapi.DiagResult{{Status: "fail"}}},
		DiagErr:           fmt.Errorf("diag error"),
		HealthResponse:    dcgmapi.HealthResponse{Incidents: []dcgmapi.Incident{{}}},
		HealthErr:         fmt.Errorf("health error"),
		FieldValues:       []dcgmapi.FieldValue_v2{{FieldID: dcgmapi.DCGM_FI_CUDA_DRIVER_VERSION}},
		FieldErr:          fmt.Errorf("fields error"),
		DeviceCount:       8,
		DeviceCountErr:    fmt.Errorf("device count error"),
	}

	pchan := dcgm.PolicyViolationChannel()
	assert.EqualValues(t, dcgm.PolicyChan, pchan)

	init, err := dcgm.Reconcile(context.TODO())
	assert.Equal(t, dcgm.ReconcileJustInit, init)
	assert.ErrorIs(t, dcgm.ReconcileErr, err)

	diag, err := dcgm.RunDiag(0)
	assert.Equal(t, dcgm.DiagResults, diag)
	assert.ErrorIs(t, dcgm.DiagErr, err)

	res, err := dcgm.HealthCheck()
	assert.Equal(t, dcgm.HealthResponse, res)
	assert.ErrorIs(t, dcgm.HealthErr, err)

	values, err := dcgm.GetValuesSince(time.Now())
	assert.Equal(t, dcgm.FieldValues, values)
	assert.ErrorIs(t, dcgm.FieldErr, err)

	count, err := dcgm.GetDeviceCount()
	assert.Equal(t, dcgm.DeviceCount, count)
	assert.ErrorIs(t, dcgm.DeviceCountErr, err)
}
