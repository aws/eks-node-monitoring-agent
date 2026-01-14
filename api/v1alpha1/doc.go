// +k8s:deepcopy-gen=package,register
// +groupName=eks.amazonaws.com

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "eks.amazonaws.com", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
)

func init() {
	SchemeBuilder.Register(&NodeDiagnostic{}, &NodeDiagnosticList{})
}
