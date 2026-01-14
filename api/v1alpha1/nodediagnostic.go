package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	KindNodeDiagnostic = "NodeDiagnostic"
)

// The name of the NodeDiagnostic resource is meant to match the name of the
// node which should perform the diagnostic tasks
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type NodeDiagnostic struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeDiagnosticSpec   `json:"spec,omitempty"`
	Status            NodeDiagnosticStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeDiagnosticList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeDiagnostic `json:"items"`
}

type NodeDiagnosticSpec struct {
	*LogCapture `json:"logCapture,omitempty"`
}

type NodeDiagnosticStatus struct {
	CaptureStatuses []CaptureStatus `json:"captureStatuses,omitempty"`
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`
}

// LogCapture is a definition for a diagnostic task that will package relevant
// logs and stats into a tarball and deliver it to a provided destination.
type LogCapture struct {
	UploadDestination `json:"destination"`
	// Categories are log source groups for the LogCapture task.
	// +optional
	// +kubebuilder:default={All}
	Categories []LogCategory `json:"categories"`
}

// UploadDestination is a URL describing where to deliver a diagnostic artifact.
type UploadDestination string

// LogCategory is a grouping of log sources to read from when performing a
// LogCapture task.
// +kubebuilder:validation:Enum={Base,Device,Networking,Runtime,System,All}
type LogCategory string

const (
	LogCategoryBase       = "Base"
	LogCategoryDevice     = "Device"
	LogCategoryNetworking = "Networking"
	LogCategoryRuntime    = "Runtime"
	LogCategorySystem     = "System"
	LogCategoryAll        = "All"
)
