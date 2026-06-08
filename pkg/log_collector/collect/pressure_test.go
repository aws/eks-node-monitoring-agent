package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
)

func TestPressureCollect(t *testing.T) {
	const (
		cpuContent = "some avg10=0.10 avg60=0.20 avg300=0.30 total=12345\n" +
			"full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
		memContent = "some avg10=1.00 avg60=2.00 avg300=3.00 total=98765\n" +
			"full avg10=0.50 avg60=1.00 avg300=1.50 total=54321\n"
		ioContent = "some avg10=4.00 avg60=5.00 avg300=6.00 total=11111\n" +
			"full avg10=2.00 avg60=2.50 avg300=3.00 total=22222\n"
	)

	tests := []struct {
		name       string
		populate   map[string]string // path under /proc/pressure -> contents
		wantFiles  map[string]string // path under destination -> expected contents
		wantAbsent []string          // paths under destination that must not exist
	}{
		{
			name: "all three pressure files present",
			populate: map[string]string{
				"cpu":    cpuContent,
				"memory": memContent,
				"io":     ioContent,
			},
			wantFiles: map[string]string{
				"system/pressure_cpu.txt":    cpuContent,
				"system/pressure_memory.txt": memContent,
				"system/pressure_io.txt":     ioContent,
			},
		},
		{
			name:     "psi disabled (no files present)",
			populate: nil,
			wantAbsent: []string{
				"system/pressure_cpu.txt",
				"system/pressure_memory.txt",
				"system/pressure_io.txt",
			},
		},
		{
			name: "only cpu present (partial kernel support)",
			populate: map[string]string{
				"cpu": cpuContent,
			},
			wantFiles: map[string]string{
				"system/pressure_cpu.txt": cpuContent,
			},
			wantAbsent: []string{
				"system/pressure_memory.txt",
				"system/pressure_io.txt",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dst := t.TempDir()

			pressureDir := filepath.Join(root, "proc", "pressure")
			if err := os.MkdirAll(pressureDir, 0o755); err != nil {
				t.Fatalf("setup: mkdir pressure dir: %v", err)
			}
			for name, content := range tc.populate {
				path := filepath.Join(pressureDir, name)
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatalf("setup: write %s: %v", path, err)
				}
			}

			acc, err := collect.NewAccessor(collect.Config{
				Root:        root,
				Destination: dst,
			})
			if err != nil {
				t.Fatalf("NewAccessor: %v", err)
			}

			if err := (&collect.Pressure{}).Collect(acc); err != nil {
				t.Fatalf("Collect: %v", err)
			}

			for rel, want := range tc.wantFiles {
				got, err := os.ReadFile(filepath.Join(dst, rel))
				if err != nil {
					t.Errorf("expected %s to be written, got err %v", rel, err)
					continue
				}
				if string(got) != want {
					t.Errorf("%s: contents mismatch\nwant: %q\n got: %q", rel, want, string(got))
				}
			}
			for _, rel := range tc.wantAbsent {
				if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
					t.Errorf("expected %s to be absent, got err=%v", rel, err)
				}
			}
		})
	}
}
