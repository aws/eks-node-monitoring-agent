package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
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
