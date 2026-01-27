//go:build !darwin

package fake

import (
	"context"
	"time"

	dcgmapi "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia/dcgm"
)

var _ dcgm.DCGM = &FakeDcgm{}

type FakeDcgm struct {
	PolicyChan        chan dcgmapi.PolicyViolation
	ReconcileErr      error
	ReconcileJustInit bool
	DiagResults       dcgmapi.DiagResults
	DiagErr           error
	HealthResponse    dcgmapi.HealthResponse
	HealthErr         error
	FieldValues       []dcgmapi.FieldValue_v2
	FieldErr          error
	DeviceCount       uint
	DeviceCountErr    error
}

func (m *FakeDcgm) Reconcile(context.Context) (bool, error) {
	return m.ReconcileJustInit, m.ReconcileErr
}

func (m *FakeDcgm) PolicyViolationChannel() <-chan dcgmapi.PolicyViolation {
	return m.PolicyChan
}

func (m *FakeDcgm) RunDiag(dcgmapi.DiagType) (dcgmapi.DiagResults, error) {
	return m.DiagResults, m.DiagErr
}

func (m *FakeDcgm) HealthCheck() (dcgmapi.HealthResponse, error) {
	return m.HealthResponse, m.HealthErr
}

func (m *FakeDcgm) GetValuesSince(time.Time) ([]dcgmapi.FieldValue_v2, error) {
	return m.FieldValues, m.FieldErr
}

func (m *FakeDcgm) GetDeviceCount() (uint, error) {
	return m.DeviceCount, m.DeviceCountErr
}
