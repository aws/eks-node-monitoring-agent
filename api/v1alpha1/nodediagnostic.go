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
	*LogCapture     `json:"logCapture,omitempty"`
	*PacketCapture  `json:"packetCapture,omitempty"`
	*AutoLogCapture `json:"autoLogCapture,omitempty"`
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

// AutoLogCapture configures automatic log capture when a managed node
// condition fails. Unlike logCapture and packetCapture, the NodeDiagnostic
// that holds this field does not need to be named after a node. Any name
// works. If more than one NodeDiagnostic sets this field, the newest one
// is used.
type AutoLogCapture struct {
	// Bucket is the S3 bucket that receives capture bundles. Omit to turn
	// automatic capture off.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`
	Bucket string `json:"bucket,omitempty"`
}

// UploadDestination is a URL describing where to deliver a diagnostic artifact.
// This can be set to "node" to temporarily store logs on the node for later collection.
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

// PacketCaptureMode defines the capture implementation to use
type PacketCaptureMode string

const (
	ModeTcpdump PacketCaptureMode = "tcpdump"
)

// PacketCapture defines configuration for capturing network traffic
type PacketCapture struct {
	// Mode specifies which implementation to use
	// +kubebuilder:validation:Enum=tcpdump
	// +optional
	// +kubebuilder:default=tcpdump
	Mode PacketCaptureMode `json:"mode"`

	// Upload provides upload configuration using presigned POST
	// +kubebuilder:validation:Required
	Upload PacketCaptureUpload `json:"upload"`

	// Interface specifies which network interface to capture from
	// If empty, captures from all interfaces (equivalent to "any")
	// +optional
	Interface string `json:"interface,omitempty"`

	// Duration specifies how long to capture traffic
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=^\d+[smh]$
	Duration string `json:"duration"`

	// Filter specifies capture filter expression (tcpdump syntax)
	// +optional
	Filter string `json:"filter,omitempty"`

	// ChunkSizeMB is the max size of each capture file in MB before rotation
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=10
	// +optional
	ChunkSizeMB int `json:"chunkSizeMB,omitempty"`
}

// PacketCaptureUpload defines upload config using presigned POST
type PacketCaptureUpload struct {
	// URL is the S3 bucket endpoint URL
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Fields contains form fields from presigned POST
	// +kubebuilder:validation:Required
	Fields map[string]string `json:"fields"`
}
