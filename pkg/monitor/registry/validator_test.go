package registry

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
)

func TestValidatePlugin(t *testing.T) {
	tests := []struct {
		name    string
		plugin  MonitorPlugin
		wantErr bool
	}{
		{
			name: "valid plugin",
			plugin: &testPlugin{
				name:     "test",
				monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
			},
			wantErr: false,
		},
		{
			name:    "nil plugin",
			plugin:  nil,
			wantErr: true,
		},
		{
			name: "empty name",
			plugin: &testPlugin{
				name:     "",
				monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
			},
			wantErr: true,
		},
		{
			name: "no monitors",
			plugin: &testPlugin{
				name:     "test",
				monitors: []monitor.Monitor{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlugin(tt.plugin)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePlugin() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMonitor(t *testing.T) {
	tests := []struct {
		name    string
		monitor monitor.Monitor
		wantErr bool
	}{
		{
			name:    "valid monitor",
			monitor: &testMonitor{name: "test"},
			wantErr: false,
		},
		{
			name:    "nil monitor",
			monitor: nil,
			wantErr: true,
		},
		{
			name:    "empty name",
			monitor: &testMonitor{name: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMonitor(tt.monitor)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMonitor() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition monitor.Condition
		wantErr   bool
	}{
		{
			name: "valid info condition",
			condition: monitor.Condition{
				Reason:         "TestReason",
				Message:        "Test message",
				Severity:       monitor.SeverityInfo,
				MinOccurrences: 1,
			},
			wantErr: false,
		},
		{
			name: "empty reason",
			condition: monitor.Condition{
				Reason:   "",
				Message:  "Test",
				Severity: monitor.SeverityInfo,
			},
			wantErr: true,
		},
		{
			name: "invalid severity",
			condition: monitor.Condition{
				Reason:   "Test",
				Message:  "Test",
				Severity: "Invalid",
			},
			wantErr: true,
		},
		{
			name: "negative min occurrences",
			condition: monitor.Condition{
				Reason:         "Test",
				Message:        "Test",
				Severity:       monitor.SeverityInfo,
				MinOccurrences: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCondition(tt.condition)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCondition() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAndRegister(t *testing.T) {
	// Reset global registry for test
	globalRegistry = NewRegistry()

	plugin := &testPlugin{
		name:     "test",
		monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
	}

	if err := ValidateAndRegister(plugin); err != nil {
		t.Fatalf("ValidateAndRegister() failed: %v", err)
	}

	if _, ok := GlobalRegistry().Get("test"); !ok {
		t.Error("expected plugin to be registered")
	}
}
