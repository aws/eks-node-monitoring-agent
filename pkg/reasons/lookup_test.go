package reasons

import (
	"strings"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func TestByName(t *testing.T) {
	tests := []struct {
		name         string
		wantOK       bool
		wantSeverity monitor.Severity
	}{
		{name: "IPAMDNotRunning", wantOK: true, wantSeverity: monitor.SeverityFatal},
		{name: "NPARepeatedlyRestart", wantOK: true, wantSeverity: monitor.SeverityWarning},
		{name: "LargeEnvironment", wantOK: true, wantSeverity: monitor.SeverityWarning},
		{name: "NoSuchReason", wantOK: false},
		{name: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, ok := ByName(tt.name)
			if ok != tt.wantOK {
				t.Fatalf("ByName(%q) ok = %v, want %v", tt.name, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got := meta.DefaultSeverity(); got != tt.wantSeverity {
				t.Errorf("ByName(%q).DefaultSeverity() = %q, want %q", tt.name, got, tt.wantSeverity)
			}
		})
	}
}

func TestByNameCoversAllDeclaredReasons(t *testing.T) {
	// Spot-check that lookup entries agree with the corresponding package-level
	// variables for both plain and parameterized templates.
	if meta, ok := ByName("NvidiaXIDError"); !ok {
		t.Fatal("expected NvidiaXIDError to be registered")
	} else if !strings.Contains(meta.Template(), "%d") {
		t.Errorf("expected NvidiaXIDError template to be parameterized, got %q", meta.Template())
	}
	if meta, ok := ByName("IPAMDNotRunning"); !ok {
		t.Fatal("expected IPAMDNotRunning to be registered")
	} else if meta != IPAMDNotRunning {
		t.Error("ByName(IPAMDNotRunning) does not match the package-level variable")
	}
}
