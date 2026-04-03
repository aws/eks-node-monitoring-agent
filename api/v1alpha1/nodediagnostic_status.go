package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CaptureStatus describes the type and state of a capture task.
type CaptureStatus struct {
	Type  CaptureType  `json:"type"`
	State CaptureState `json:"state"`
}

// The set of diagnostic tasks supported by the NodeDiagnostic resource.
type CaptureType string

const (
	CaptureTypeLog    CaptureType = "Log"
	CaptureTypePacket CaptureType = "Packet"
)

type CaptureState struct {
	// +optional
	Running *CaptureStateRunning `json:"running"`
	// +optional
	Completed *CaptureStateCompleted `json:"completed"`
}

type CaptureStateRunning struct {
	StartedAt metav1.Time `json:"startedAt"`
	// CaptureID is the unique identifier for this capture session.
	// Only set for packet captures.
	// +optional
	CaptureID string `json:"captureID,omitempty"`
}

type CaptureStateCompleted struct {
	Reason     string      `json:"reason"`
	Message    string      `json:"message"`
	StartedAt  metav1.Time `json:"startedAt"`
	FinishedAt metav1.Time `json:"finishedAt"`
	// CaptureID is the unique identifier for this capture, used for log correlation
	// and S3 path isolation.
	// +optional
	CaptureID string `json:"captureID,omitempty"`
	// ObservedGeneration records which spec generation this capture executed against.
	// Used to detect spec changes and allow re-execution without delete + recreate.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

const (
	CaptureStateFailure           = "Failure"
	CaptureStateSuccess           = "Success"
	CaptureStateSuccessWithErrors = "SuccessWithErrors"
)

func (c *NodeDiagnosticStatus) GetCaptureStatus(captureType CaptureType) *CaptureStatus {
	for _, status := range c.CaptureStatuses {
		if status.Type == captureType {
			return &status
		}
	}
	return nil
}

func (c *NodeDiagnosticStatus) SetCaptureStatus(captureStatus CaptureStatus) {
	for i, status := range c.CaptureStatuses {
		if status.Type == captureStatus.Type {
			c.CaptureStatuses[i].State = captureStatus.State
			return
		}
	}
	c.CaptureStatuses = append(c.CaptureStatuses, captureStatus)
}
