package neuron

import (
	"context"
	"testing"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
)

// mockManager implements monitor.Manager for testing
type mockManager struct {
	err           error
	notifications []monitor.Condition
}

func (m *mockManager) Subscribe(rType resource.Type, rParts []resource.Part) (<-chan string, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan string, 10)
	return ch, nil
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	if m.err != nil {
		return m.err
	}
	m.notifications = append(m.notifications, condition)
	return nil
}

func (m *mockManager) GetNotifications() []monitor.Condition {
	return m.notifications
}

func TestNeuronMonitor_Name(t *testing.T) {
	m := &neuronMonitor{}
	if got := m.Name(); got != "neuron" {
		t.Errorf("Name() = %v, want %v", got, "neuron")
	}
}

func TestNeuronMonitor_Conditions(t *testing.T) {
	m := &neuronMonitor{}
	conditions := m.Conditions()
	if len(conditions) != 0 {
		t.Errorf("Conditions() returned %d conditions, want 0", len(conditions))
	}
}

func TestNeuronMonitor_HandleNeuron(t *testing.T) {
	tests := []struct {
		name          string
		dmesgLine     string
		wantCondition bool
		wantReason    string
	}{
		{
			name:          "SRAM uncorrectable error",
			dmesgLine:     "kernel: NEURON_HW_ERR=SRAM_UNCORRECTABLE_ERROR affecting neuron devices",
			wantCondition: true,
			wantReason:    "NeuronSRAMUncorrectableError",
		},
		{
			name:          "NC uncorrectable error",
			dmesgLine:     "kernel: NEURON_HW_ERR=NC_UNCORRECTABLE_ERROR affecting neuron devices",
			wantCondition: true,
			wantReason:    "NeuronNCUncorrectableError",
		},
		{
			name:          "HBM uncorrectable error",
			dmesgLine:     "kernel: NEURON_HW_ERR=HBM_UNCORRECTABLE_ERROR affecting neuron devices",
			wantCondition: true,
			wantReason:    "NeuronHBMUncorrectableError",
		},
		{
			name:          "DMA error",
			dmesgLine:     "kernel: NEURON_HW_ERR=DMA_ERROR affecting neuron devices",
			wantCondition: true,
			wantReason:    "NeuronDMAError",
		},
		{
			name:          "non-neuron error",
			dmesgLine:     "kernel: some other error message",
			wantCondition: false,
		},
		{
			name:          "empty line",
			dmesgLine:     "",
			wantCondition: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &mockManager{}
			m := &neuronMonitor{manager: mgr}

			err := m.handleNeuron(tt.dmesgLine)
			if err != nil {
				t.Errorf("handleNeuron() error = %v", err)
				return
			}

			notifications := mgr.GetNotifications()
			if tt.wantCondition {
				if len(notifications) != 1 {
					t.Errorf("expected 1 notification, got %d", len(notifications))
					return
				}
				if notifications[0].Reason != tt.wantReason {
					t.Errorf("expected reason %s, got %s", tt.wantReason, notifications[0].Reason)
				}
			} else {
				if len(notifications) != 0 {
					t.Errorf("expected no notifications, got %d", len(notifications))
				}
			}
		})
	}
}

func TestNeuronMonitor_GetNeuronMonitorRules(t *testing.T) {
	m := &neuronMonitor{}
	rules := m.getNeuronMonitorRules()

	// Verify we have all 4 expected rules
	expectedRules := 4
	if len(rules) != expectedRules {
		t.Errorf("expected %d rules, got %d", expectedRules, len(rules))
	}

	// Verify each rule has a valid regex and condition
	for i, rule := range rules {
		if rule.regex == nil {
			t.Errorf("rule %d has nil regex", i)
		}
		if rule.condition.Reason == "" {
			t.Errorf("rule %d has empty condition reason", i)
		}
		if rule.condition.Severity == "" {
			t.Errorf("rule %d has empty condition severity", i)
		}
	}

	// Verify specific patterns match expected errors
	testPatterns := []struct {
		pattern string
		reason  string
	}{
		{"kernel: NEURON_HW_ERR=SRAM_UNCORRECTABLE_ERROR affecting neuron devices", "NeuronSRAMUncorrectableError"},
		{"kernel: NEURON_HW_ERR=NC_UNCORRECTABLE_ERROR affecting neuron devices", "NeuronNCUncorrectableError"},
		{"kernel: NEURON_HW_ERR=HBM_UNCORRECTABLE_ERROR affecting neuron devices", "NeuronHBMUncorrectableError"},
		{"kernel: NEURON_HW_ERR=DMA_ERROR affecting neuron devices", "NeuronDMAError"},
	}

	for _, tp := range testPatterns {
		found := false
		for _, rule := range rules {
			if rule.regex.MatchString(tp.pattern) && rule.condition.Reason == tp.reason {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no rule found matching pattern %s with reason %s", tp.pattern, tp.reason)
		}
	}
}

func TestNeuronMonitor_Register(t *testing.T) {
	mgr := &mockManager{}
	m := &neuronMonitor{}

	ctx := context.Background()
	err := m.Register(ctx, mgr)
	if err != nil {
		t.Errorf("Register() error = %v", err)
	}

	if m.manager == nil {
		t.Error("manager was not set after Register()")
	}
}

func TestNeuronMonitor_AllErrorTypes(t *testing.T) {
	// Test that all Neuron error types are properly detected
	errorTypes := []struct {
		errorType string
		reason    reasons.ReasonMeta
	}{
		{"SRAM_UNCORRECTABLE_ERROR", reasons.NeuronSRAMUncorrectableError},
		{"NC_UNCORRECTABLE_ERROR", reasons.NeuronNCUncorrectableError},
		{"HBM_UNCORRECTABLE_ERROR", reasons.NeuronHBMUncorrectableError},
		{"DMA_ERROR", reasons.NeuronDMAError},
	}

	for _, et := range errorTypes {
		t.Run(et.errorType, func(t *testing.T) {
			mgr := &mockManager{}
			m := &neuronMonitor{manager: mgr}

			dmesgLine := "kernel: NEURON_HW_ERR=" + et.errorType + " affecting neuron devices"
			err := m.handleNeuron(dmesgLine)
			if err != nil {
				t.Errorf("handleNeuron() error = %v", err)
				return
			}

			notifications := mgr.GetNotifications()
			if len(notifications) != 1 {
				t.Errorf("expected 1 notification, got %d", len(notifications))
				return
			}

			expectedReason := et.reason.Builder().Message("test").Build().Reason
			if notifications[0].Reason != expectedReason {
				t.Errorf("expected reason %s, got %s", expectedReason, notifications[0].Reason)
			}

			// Verify severity is Fatal for all Neuron errors
			if notifications[0].Severity != "Fatal" {
				t.Errorf("expected severity Fatal, got %s", notifications[0].Severity)
			}
		})
	}
}
