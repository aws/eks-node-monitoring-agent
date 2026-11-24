package collect_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
)

func writeProc(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, "proc", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup: write %s: %v", path, err)
	}
}

func newMemoryAccessor(t *testing.T, ctx context.Context, root, dst string) *collect.Accessor {
	t.Helper()
	acc, err := collect.NewAccessor(ctx, collect.Config{Root: root, Destination: dst})
	if err != nil {
		t.Fatalf("NewAccessor: %v", err)
	}
	return acc
}

func readOutput(t *testing.T, dst, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dst, rel))
	if err != nil {
		t.Fatalf("expected %s to be written: %v", rel, err)
	}
	return string(data)
}

func assertAbsent(t *testing.T, dst, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
		t.Errorf("expected %s to be absent, got err=%v", rel, err)
	}
}

func TestMemoryCollectTakesTwoVmstatSamples(t *testing.T) {
	const meminfo = "MemTotal:        7918528 kB\nMemAvailable:    2238640 kB\n"
	root, dst := t.TempDir(), t.TempDir()
	writeProc(t, root, "meminfo", meminfo)
	writeProc(t, root, "vmstat", "pgmajfault 100\n")

	acc := newMemoryAccessor(t, context.Background(), root, dst)

	// change the file while the collector waits, so the second sample must be
	// read after the interval and not reuse the first.
	go func() {
		time.Sleep(20 * time.Millisecond)
		// t.Fatal must not be called from this goroutine, so write directly.
		if err := os.WriteFile(filepath.Join(root, "proc", "vmstat"), []byte("pgmajfault 250\n"), 0o644); err != nil {
			t.Errorf("rewrite vmstat: %v", err)
		}
	}()
	if err := (&collect.Memory{Interval: 200 * time.Millisecond}).Collect(acc); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := readOutput(t, dst, "system/meminfo.txt"); got != meminfo {
		t.Errorf("meminfo: want %q, got %q", meminfo, got)
	}
	if got := readOutput(t, dst, "system/vmstat_1.txt"); got != "pgmajfault 100\n" {
		t.Errorf("vmstat_1: got %q", got)
	}
	if got := readOutput(t, dst, "system/vmstat_2.txt"); got != "pgmajfault 250\n" {
		t.Errorf("vmstat_2: got %q", got)
	}

	lines := strings.Split(strings.TrimSpace(readOutput(t, dst, "system/vmstat_times.txt")), "\n")
	if len(lines) != 2 {
		t.Fatalf("vmstat_times: want 2 lines, got %q", lines)
	}
	var times [2]time.Time
	for i, want := range []string{"vmstat_1.txt", "vmstat_2.txt"} {
		name, stamp, ok := strings.Cut(lines[i], " ")
		if !ok || name != want {
			t.Fatalf("vmstat_times line %d: got %q", i, lines[i])
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t.Fatalf("vmstat_times line %d: %v", i, err)
		}
		times[i] = parsed
	}
	if gap := times[1].Sub(times[0]); gap < 200*time.Millisecond {
		t.Errorf("samples are %s apart, want at least the interval", gap)
	}
}

func TestMemoryCollectMissingFiles(t *testing.T) {
	root, dst := t.TempDir(), t.TempDir()
	acc := newMemoryAccessor(t, context.Background(), root, dst)

	start := time.Now()
	if err := (&collect.Memory{Interval: time.Hour}).Collect(acc); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if time.Since(start) > time.Minute {
		t.Error("waited for a second sample although /proc/vmstat is missing")
	}
	for _, rel := range []string{"system/meminfo.txt", "system/vmstat_1.txt", "system/vmstat_2.txt", "system/vmstat_times.txt"} {
		assertAbsent(t, dst, rel)
	}
}

func TestMemoryCollectStopsWhenCancelled(t *testing.T) {
	root, dst := t.TempDir(), t.TempDir()
	writeProc(t, root, "vmstat", "pgmajfault 100\n")

	ctx, cancel := context.WithCancel(context.Background())
	acc := newMemoryAccessor(t, ctx, root, dst)
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	err := (&collect.Memory{Interval: time.Hour}).Collect(acc)
	if err == nil {
		t.Fatal("expected the cancellation to be reported")
	}
	if time.Since(start) > time.Minute {
		t.Fatal("did not stop waiting when the context was cancelled")
	}
	if got := readOutput(t, dst, "system/vmstat_1.txt"); got != "pgmajfault 100\n" {
		t.Errorf("vmstat_1: got %q", got)
	}
	assertAbsent(t, dst, "system/vmstat_2.txt")
	if got := readOutput(t, dst, "system/vmstat_times.txt"); !strings.HasPrefix(got, "vmstat_1.txt ") || strings.Contains(got, "vmstat_2.txt") {
		t.Errorf("vmstat_times: got %q", got)
	}
}
