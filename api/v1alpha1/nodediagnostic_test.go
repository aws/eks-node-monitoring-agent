package v1alpha1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestNodeDiagnosticSpecAutoLogCaptureUnmarshal(t *testing.T) {
	in := []byte(`{"autoLogCapture":{"bucket":"my-capture-bucket"}}`)
	var spec NodeDiagnosticSpec
	if err := json.Unmarshal(in, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.AutoLogCapture == nil {
		t.Fatal("AutoLogCapture should be set when autoLogCapture is present")
	}
	if spec.AutoLogCapture.Bucket != "my-capture-bucket" {
		t.Errorf("bucket = %q, want %q", spec.AutoLogCapture.Bucket, "my-capture-bucket")
	}
}

func TestNodeDiagnosticSpecLogCaptureOnlyLeavesAutoLogCaptureNil(t *testing.T) {
	in := []byte(`{"logCapture":{"destination":"node"}}`)
	var spec NodeDiagnosticSpec
	if err := json.Unmarshal(in, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.LogCapture == nil {
		t.Fatal("LogCapture should be set when logCapture is present")
	}
	if spec.AutoLogCapture != nil {
		t.Errorf("AutoLogCapture should be nil when only logCapture is set, got %+v", spec.AutoLogCapture)
	}
}

func TestNodeDiagnosticSpecNilAutoLogCaptureOmitted(t *testing.T) {
	spec := NodeDiagnosticSpec{
		LogCapture: &LogCapture{UploadDestination: "node"},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "autoLogCapture") {
		t.Errorf("autoLogCapture should be omitted when nil, got %s", data)
	}
}

// The CEL rule needs a literal name, so this keeps it from drifting from
// AutoLogCaptureObjectName.
func TestNodeDiagnosticCRDRestrictsAutoLogCaptureName(t *testing.T) {
	data, err := os.ReadFile("../crds/eks.amazonaws.com_nodediagnostics.yaml")
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	var schema *apiextensionsv1.JSONSchemaProps
	for _, v := range crd.Spec.Versions {
		if v.Name == "v1alpha1" && v.Schema != nil {
			schema = v.Schema.OpenAPIV3Schema
		}
	}
	if schema == nil {
		t.Fatal("CRD has no v1alpha1 openAPIV3Schema")
	}
	if len(schema.XValidations) != 2 {
		t.Fatalf("root XValidations = %d, want 2", len(schema.XValidations))
	}
	if want := "'" + AutoLogCaptureObjectName + "'"; !strings.Contains(schema.XValidations[0].Rule, want) {
		t.Errorf("first rule %q does not contain %s", schema.XValidations[0].Rule, want)
	}
	for _, field := range []string{"logCapture", "packetCapture"} {
		if !strings.Contains(schema.XValidations[1].Rule, field) {
			t.Errorf("second rule %q does not mention %s", schema.XValidations[1].Rule, field)
		}
	}
}
