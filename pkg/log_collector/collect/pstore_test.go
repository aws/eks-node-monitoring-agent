package collect_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
)

func TestPstoreCollect(t *testing.T) {
	const (
		dmesgContent   = "Panic#1 Part1\nsome kernel oops output\n"
		consoleContent = "console-ramoops-0 contents\n"
		archivedDmesg  = "dmesg.txt from systemd-pstore\n"
	)

	tests := []struct {
		name string
		// sysFs populates /sys/fs/pstore under the fake host root. A nil map
		// means the directory is absent; an empty map means present-but-empty.
		sysFs map[string]string
		// systemd populates /var/lib/systemd/pstore under the fake host root.
		// Nil means absent, empty means present-but-empty.
		systemd map[string]string
		// wantFiles maps a destination path to a substring that must appear in
		// that file. Substring lets the "present but empty" and "not present"
		// note cases share a stable assertion without pinning the exact wording.
		wantFiles map[string]string
		// wantAbsent lists destination paths that must not exist. This is what
		// enforces the "populated dir OR note file, never both" invariant.
		wantAbsent []string
	}{
		{
			name: "both sources present with content",
			sysFs: map[string]string{
				"dmesg-efi-165000000000000001": dmesgContent,
				"console-ramoops-0":            consoleContent,
			},
			systemd: map[string]string{
				"000001-dmesg.txt": archivedDmesg,
			},
			wantFiles: map[string]string{
				"pstore/sys_fs_pstore/dmesg-efi-165000000000000001": dmesgContent,
				"pstore/sys_fs_pstore/console-ramoops-0":            consoleContent,
				"pstore/systemd_pstore/000001-dmesg.txt":            archivedDmesg,
			},
			wantAbsent: []string{
				"pstore/sys_fs_pstore.txt",
				"pstore/systemd_pstore.txt",
			},
		},
		{
			name:    "both sources present but empty",
			sysFs:   map[string]string{},
			systemd: map[string]string{},
			wantFiles: map[string]string{
				"pstore/sys_fs_pstore.txt":  "present but empty",
				"pstore/systemd_pstore.txt": "present but empty",
			},
			wantAbsent: []string{
				"pstore/sys_fs_pstore",
				"pstore/systemd_pstore",
			},
		},
		{
			name:    "neither source present",
			sysFs:   nil,
			systemd: nil,
			wantFiles: map[string]string{
				"pstore/sys_fs_pstore.txt":  "not present",
				"pstore/systemd_pstore.txt": "not present",
			},
			wantAbsent: []string{
				"pstore/sys_fs_pstore",
				"pstore/systemd_pstore",
			},
		},
		{
			name: "sys_fs_pstore populated, systemd_pstore absent",
			sysFs: map[string]string{
				"dmesg-efi-165000000000000002": dmesgContent,
			},
			systemd: nil,
			wantFiles: map[string]string{
				"pstore/sys_fs_pstore/dmesg-efi-165000000000000002": dmesgContent,
				"pstore/systemd_pstore.txt":                         "not present",
			},
			wantAbsent: []string{
				"pstore/sys_fs_pstore.txt",
				"pstore/systemd_pstore",
			},
		},
		{
			name:    "sys_fs_pstore absent, systemd_pstore empty",
			sysFs:   nil,
			systemd: map[string]string{
				// present but empty
			},
			wantFiles: map[string]string{
				"pstore/sys_fs_pstore.txt":  "not present",
				"pstore/systemd_pstore.txt": "present but empty",
			},
			wantAbsent: []string{
				"pstore/sys_fs_pstore",
				"pstore/systemd_pstore",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dst := t.TempDir()

			populate := func(rel string, contents map[string]string) {
				if contents == nil {
					// nil means the directory itself does not exist on the host.
					return
				}
				dir := filepath.Join(root, rel)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("setup: mkdir %s: %v", dir, err)
				}
				for name, body := range contents {
					path := filepath.Join(dir, name)
					if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
						t.Fatalf("setup: write %s: %v", path, err)
					}
				}
			}
			populate("sys/fs/pstore", tc.sysFs)
			populate("var/lib/systemd/pstore", tc.systemd)

			acc, err := collect.NewAccessor(context.Background(), collect.Config{
				Root:        root,
				Destination: dst,
			})
			if err != nil {
				t.Fatalf("NewAccessor: %v", err)
			}

			if err := (&collect.Pstore{}).Collect(acc); err != nil {
				t.Fatalf("Collect: %v", err)
			}

			for rel, wantSubstr := range tc.wantFiles {
				body, err := os.ReadFile(filepath.Join(dst, rel))
				if err != nil {
					t.Errorf("expected %s to be written: %v", rel, err)
					continue
				}
				if !strings.Contains(string(body), wantSubstr) {
					t.Errorf("%s: expected to contain %q, got %q", rel, wantSubstr, string(body))
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

func TestPstoreCollectNonDirectorySource(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "sys", "fs"), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sys", "fs", "pstore"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("setup: write: %v", err)
	}

	acc, err := collect.NewAccessor(context.Background(), collect.Config{
		Root:        root,
		Destination: dst,
	})
	if err != nil {
		t.Fatalf("NewAccessor: %v", err)
	}

	if err := (&collect.Pstore{}).Collect(acc); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dst, "pstore/sys_fs_pstore.txt"))
	if err != nil {
		t.Fatalf("expected sys_fs_pstore.txt note: %v", err)
	}
	if !strings.Contains(string(body), "not a directory") {
		t.Errorf("expected note to flag non-directory source, got %q", string(body))
	}
	if _, err := os.Stat(filepath.Join(dst, "pstore/sys_fs_pstore")); !os.IsNotExist(err) {
		t.Errorf("expected pstore/sys_fs_pstore to be absent for non-directory source, got err=%v", err)
	}
}
