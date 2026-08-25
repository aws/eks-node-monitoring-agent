package collect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

const (
	defaultName = "/usr/bin/aws-eks-na-cli"
	published   = "/run/aws-network-policy-agent/aws-eks-na-cli"
)

// TestChooseNetworkPolicyCLI pins the selection table: use a binary only when the
// family is confirmed (NPA symlink) or a single binary is installed (EC2); with both
// binaries and no symlink (Auto Mode), skip.
func TestChooseNetworkPolicyCLI(t *testing.T) {
	tests := []struct {
		name          string
		havePublished bool
		haveDefault   bool
		haveV6Named   bool
		wantCLI       string
		wantOK        bool
		reasonHas     string
	}{
		{
			name:          "published symlink present -> use it",
			havePublished: true,
			haveDefault:   true,
			haveV6Named:   true,
			wantCLI:       published,
			wantOK:        true,
			reasonHas:     "NPA-published family-correct symlink present",
		},
		{
			name:          "published symlink present, single binary (EC2 + NPA link) -> use it",
			havePublished: true,
			haveDefault:   true,
			haveV6Named:   false,
			wantCLI:       published,
			wantOK:        true,
			reasonHas:     "NPA-published family-correct symlink present",
		},
		{
			name:        "no CLI installed -> skip",
			haveDefault: false,
			haveV6Named: false,
			wantCLI:     "",
			wantOK:      false,
			reasonHas:   "not installed",
		},
		{
			name:        "EC2 (only default-named, no symlink) -> use it",
			haveDefault: true,
			haveV6Named: false,
			wantCLI:     defaultName,
			wantOK:      true,
			reasonHas:   "single binary",
		},
		{
			name:        "only v6-named present (unexpected layout) -> skip",
			haveDefault: false,
			haveV6Named: true,
			wantCLI:     "",
			wantOK:      false,
			reasonHas:   "family unconfirmed",
		},
		{
			name:        "Auto (both binaries, no symlink) -> skip, family unconfirmed",
			haveDefault: true,
			haveV6Named: true,
			wantCLI:     "",
			wantOK:      false,
			reasonHas:   "no NPA-published symlink",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCLI, reason, gotOK := chooseNetworkPolicyCLI(published, tt.havePublished, defaultName, tt.haveDefault, tt.haveV6Named)
			if gotCLI != tt.wantCLI {
				t.Errorf("cliPath = %q, want %q", gotCLI, tt.wantCLI)
			}
			if gotOK != tt.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !strings.Contains(reason, tt.reasonHas) {
				t.Errorf("reason %q does not contain %q", reason, tt.reasonHas)
			}
		})
	}
}

// uniqueMapIDs must dedup, drop blanks, and sort so the bundle is deterministic.
func TestUniqueMapIDs(t *testing.T) {
	loaded := strings.Join([]string{
		"Map Name:  ingress_map",
		"Map ID:  21",
		"Map Name:  aws_conntrack_map",
		"Map ID:  18",
		"Map Name:  policy_events",
		"Map ID:  19",
		"Map Name:  aws_conntrack_map", // same global map referenced again
		"Map ID:  18",
		"garbage line with no id",
		"Map ID:  ", // blank id, must be skipped
	}, "\n")
	got := uniqueMapIDs(loaded)
	want := []string{"18", "19", "21"} // deduped (18 once) + sorted
	if len(got) != len(want) {
		t.Fatalf("uniqueMapIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uniqueMapIDs[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

// TestFindPublishedNetworkPolicyCLI exercises the filesystem seam: link and target
// are resolved under acc.cfg.Root, and a dangling link reads as absent (the skip
// fail-safe).
func TestFindPublishedNetworkPolicyCLI(t *testing.T) {
	// Layout under a fake Root:
	//   <root>/usr/bin/aws-eks-na-cli-v6        (the family target build)
	//   <root>/run/aws-network-policy-agent/aws-eks-na-cli -> /usr/bin/aws-eks-na-cli-v6 (abs)
	newRoot := func(t *testing.T, target string, linkTo string) string {
		t.Helper()
		root := t.TempDir()
		binDir := filepath.Join(root, "usr", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if target != "" {
			if err := os.WriteFile(filepath.Join(binDir, target), []byte("stub"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		linkDir := filepath.Join(root, "run", "aws-network-policy-agent")
		if err := os.MkdirAll(linkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if linkTo != "" {
			// Absolute target, exactly as NPA's PublishFamilyCLILink writes it.
			if err := os.Symlink(linkTo, filepath.Join(linkDir, naCLIDefault)); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	t.Run("valid link with existing target resolved under Root -> found", func(t *testing.T) {
		root := newRoot(t, naCLIv6Named, "/usr/bin/"+naCLIv6Named)
		acc := &Accessor{cfg: Config{Root: root}}
		got, ok := findPublishedNetworkPolicyCLI(acc)
		if !ok {
			t.Fatal("want found for a valid link whose target exists under Root")
		}
		if want := filepath.Join(root, naCLIPublishedDir, naCLIDefault); got != want {
			t.Errorf("cliPath = %q, want the Root-prefixed link path %q", got, want)
		}
	})

	t.Run("dangling link (target missing under Root) -> absent", func(t *testing.T) {
		// Link points at /usr/bin/aws-eks-na-cli-v6 but that file is not created.
		root := newRoot(t, "", "/usr/bin/"+naCLIv6Named)
		acc := &Accessor{cfg: Config{Root: root}}
		if _, ok := findPublishedNetworkPolicyCLI(acc); ok {
			t.Error("dangling link must read as absent so map decoding is skipped")
		}
	})

	t.Run("no link at all -> absent", func(t *testing.T) {
		root := newRoot(t, naCLIv6Named, "")
		acc := &Accessor{cfg: Config{Root: root}}
		if _, ok := findPublishedNetworkPolicyCLI(acc); ok {
			t.Error("want absent when no published link exists")
		}
	})

	t.Run("absolute target resolves under Root, not NMA's own /usr/bin", func(t *testing.T) {
		// The link's absolute target /usr/bin/aws-eks-na-cli-v6 exists ONLY under
		// the fake Root, never at the process's real /usr/bin — so a Root-correct
		// resolver finds it and a Root-blind os.Stat(link) would not.
		root := newRoot(t, naCLIv6Named, "/usr/bin/"+naCLIv6Named)
		acc := &Accessor{cfg: Config{Root: root}}
		if _, ok := findPublishedNetworkPolicyCLI(acc); !ok {
			t.Error("absolute link target must be resolved under Root")
		}
	})
}

// TestNetworkPolicyEbpfInfoRecordsExecFailure checks that when a selected binary
// cannot be executed, the failure is recorded in the bundle as a
// cliExecFailedLinePrefix line and is best-effort: it is not returned as an error,
// so a denied exec does not fail the whole capture.
func TestNetworkPolicyEbpfInfoRecordsExecFailure(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	binDir := filepath.Join(root, "usr", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A single non-executable stub: selected as family-correct, but the exec then
	// fails — the state under test.
	if err := os.WriteFile(filepath.Join(binDir, naCLIDefault), []byte("not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A hand-built accessor must set ctx, or the context-bound CombinedOutput panics.
	acc := &Accessor{
		cfg:    Config{Root: root, Destination: dest, CommandTimeout: 5 * time.Second},
		ctx:    context.Background(),
		logger: logr.Discard(),
	}
	if err := networkPolicyEbpfInfo(acc); err != nil {
		t.Fatalf("a failed exec must be best-effort (recorded, not returned); got %v", err)
	}

	data, readErr := os.ReadFile(filepath.Join(dest, ebpfDataFile))
	if readErr != nil {
		t.Fatalf("reading %s: %v", ebpfDataFile, readErr)
	}
	var found bool
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, cliExecFailedLinePrefix) {
			found = true
		}
	}
	if !found {
		t.Errorf("%s must contain a line starting with %q; got:\n%s", ebpfDataFile, cliExecFailedLinePrefix, data)
	}
}
