package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	defaultName = "/usr/bin/aws-eks-na-cli"
	v6Named     = "/usr/bin/aws-eks-na-cli-v6"
	published   = "/run/aws-network-policy-agent/aws-eks-na-cli"
)

// TestChooseNetworkPolicyCLI pins the symlink-only selection model: a binary
// decodes maps only when the node's family is confirmed by the NPA-published
// symlink (or when a single binary is installed, i.e. EC2). On Auto Mode with
// both binaries but no symlink, decoding is skipped — there is no family query
// and no decode probe in this vertical. The single-binary rows are regression
// pins for EC2 behavior, which must not change.
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
			name:        "only v6-named present (unexpected layout, no symlink) -> use it",
			haveDefault: false,
			haveV6Named: true,
			wantCLI:     v6Named,
			wantOK:      true,
			reasonHas:   "unexpected layout",
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
			gotCLI, reason, gotOK := chooseNetworkPolicyCLI(published, tt.havePublished, defaultName, tt.haveDefault, v6Named, tt.haveV6Named)
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

// TestFindPublishedNetworkPolicyCLI exercises the real filesystem seam: the
// link and its target must both be resolved under acc.cfg.Root, and a dangling
// link (target missing) must read as absent — the fail-safe the whole
// symlink-skip contract depends on.
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

// TestSelectionReasonMarkerInvariant pins the contract that e2e tests rely on:
// every selection reason where ok=true contains CLISelectedMarker, and every
// reason where ok=false does not. Rewording a reason in a way that breaks this
// (which would silently break the e2e skip-vs-fail discriminator) fails here, in
// the collector's own package.
func TestSelectionReasonMarkerInvariant(t *testing.T) {
	cases := []struct {
		name          string
		havePublished bool
		haveDefault   bool
		haveV6Named   bool
	}{
		{"published", true, true, true},
		{"single default", false, true, false},
		{"v6 named only", false, false, true},
		{"both, no symlink -> skip", false, true, true},
		{"none installed -> skip", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, reason, ok := chooseNetworkPolicyCLI(published, tc.havePublished, defaultName, tc.haveDefault, v6Named, tc.haveV6Named)
			has := strings.Contains(reason, CLISelectedMarker)
			if ok && !has {
				t.Errorf("selected reason must contain %q marker; got %q", CLISelectedMarker, reason)
			}
			if !ok && has {
				t.Errorf("skip reason must not contain %q marker; got %q", CLISelectedMarker, reason)
			}
		})
	}
}
