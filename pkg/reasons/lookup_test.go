package reasons

import (
	"slices"
	"strings"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func TestByName(t *testing.T) {
	tests := []struct {
		name          string
		wantOK        bool
		wantSeverity  monitor.Severity
		wantComponent string
	}{
		{name: "IPAMDNotRunning", wantOK: true, wantSeverity: monitor.SeverityFatal, wantComponent: "ipamd"},
		{name: "NPARepeatedlyRestart", wantOK: true, wantSeverity: monitor.SeverityWarning, wantComponent: "npa"},
		{name: "LargeEnvironment", wantOK: true, wantSeverity: monitor.SeverityWarning, wantComponent: "kernel"},
		{name: "InterfaceNotUp", wantOK: true, wantSeverity: monitor.SeverityFatal, wantComponent: "networking"},
		{name: "NvidiaXIDError", wantOK: true, wantSeverity: monitor.SeverityFatal, wantComponent: "nvidia"},
		{name: "NeuronDMAError", wantOK: true, wantSeverity: monitor.SeverityFatal, wantComponent: "neuron"},
		{name: "RepeatedRestart", wantOK: true, wantSeverity: monitor.SeverityWarning, wantComponent: "container-runtime"},
		{name: "IODelays", wantOK: true, wantSeverity: monitor.SeverityWarning, wantComponent: "storage"},
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
			if got := meta.Component(); got != tt.wantComponent {
				t.Errorf("ByName(%q).Component() = %q, want %q", tt.name, got, tt.wantComponent)
			}
		})
	}
}

func TestComponents(t *testing.T) {
	want := []string{"container-runtime", "ipamd", "kernel", "networking", "neuron", "npa", "nvidia", "storage"}
	got := Components()
	if !slices.Equal(got, want) {
		t.Errorf("Components() = %v, want %v", got, want)
	}
	for _, c := range want {
		if !IsComponent(c) {
			t.Errorf("IsComponent(%q) = false, want true", c)
		}
	}
	if IsComponent("mock") {
		t.Error(`IsComponent("mock") = true, want false`)
	}
}

func TestValidateMonitorComponents(t *testing.T) {
	// The production monitor names must all be declared components; this is
	// the same check main.go runs at startup.
	if err := ValidateMonitorComponents("kernel", "networking", "storage", "container-runtime", "nvidia", "neuron"); err != nil {
		t.Errorf("production monitor names must validate, got %v", err)
	}
	err := ValidateMonitorComponents("kernel", "not-a-component")
	if err == nil {
		t.Fatal("expected error for undeclared monitor name")
	}
	if !strings.Contains(err.Error(), "not-a-component") || !strings.Contains(err.Error(), "reasons.yaml") {
		t.Errorf("error should name the offender and the ledger, got %q", err.Error())
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
