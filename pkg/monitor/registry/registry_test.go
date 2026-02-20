package registry

import (
	"context"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type testMonitor struct {
	name string
}

func (m *testMonitor) Name() string {
	return m.name
}

func (m *testMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (m *testMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	return nil
}

type testPlugin struct {
	name     string
	monitors []monitor.Monitor
}

func (p *testPlugin) Name() string {
	return p.name
}

func (p *testPlugin) Monitors() []monitor.Monitor {
	return p.monitors
}

type testPluginWithCRDs struct {
	testPlugin
	crds []*apiextensionsv1.CustomResourceDefinition
}

func (p *testPluginWithCRDs) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return p.crds
}

func TestRegistry_Register(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			err := r.Register(tt.plugin)
			if (err != nil) != tt.wantErr {
				t.Errorf("Register() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegistry_DuplicateRegistration(t *testing.T) {
	r := NewRegistry()
	plugin := &testPlugin{
		name:     "test",
		monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
	}

	if err := r.Register(plugin); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	if err := r.Register(plugin); err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()
	plugin := &testPlugin{
		name:     "test",
		monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
	}

	r.Register(plugin)

	got, ok := r.Get("test")
	if !ok {
		t.Error("expected to find plugin")
	}
	if got.Name() != "test" {
		t.Errorf("got name %s, want test", got.Name())
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("expected not to find plugin")
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()

	if len(r.List()) != 0 {
		t.Error("expected empty list")
	}

	plugin1 := &testPlugin{name: "test1", monitors: []monitor.Monitor{&testMonitor{name: "mon1"}}}
	plugin2 := &testPlugin{name: "test2", monitors: []monitor.Monitor{&testMonitor{name: "mon2"}}}

	r.Register(plugin1)
	r.Register(plugin2)

	plugins := r.List()
	if len(plugins) != 2 {
		t.Errorf("got %d plugins, want 2", len(plugins))
	}
}

func TestRegistry_AllMonitors(t *testing.T) {
	r := NewRegistry()

	plugin1 := &testPlugin{
		name: "test1",
		monitors: []monitor.Monitor{
			&testMonitor{name: "mon1"},
			&testMonitor{name: "mon2"},
		},
	}
	plugin2 := &testPlugin{
		name: "test2",
		monitors: []monitor.Monitor{
			&testMonitor{name: "mon3"},
		},
	}

	r.Register(plugin1)
	r.Register(plugin2)

	monitors := r.AllMonitors()
	if len(monitors) != 3 {
		t.Errorf("got %d monitors, want 3", len(monitors))
	}
}

func TestRegistry_AllCRDs(t *testing.T) {
	r := NewRegistry()

	// Plugin without CRDs
	plugin1 := &testPlugin{
		name:     "test1",
		monitors: []monitor.Monitor{&testMonitor{name: "mon1"}},
	}

	// Plugin with CRDs
	crd1 := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "crd1"},
	}
	crd2 := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "crd2"},
	}
	plugin2 := &testPluginWithCRDs{
		testPlugin: testPlugin{
			name:     "test2",
			monitors: []monitor.Monitor{&testMonitor{name: "mon2"}},
		},
		crds: []*apiextensionsv1.CustomResourceDefinition{crd1, crd2},
	}

	r.Register(plugin1)
	r.Register(plugin2)

	crds := r.AllCRDs()
	if len(crds) != 2 {
		t.Errorf("got %d CRDs, want 2", len(crds))
	}
}
