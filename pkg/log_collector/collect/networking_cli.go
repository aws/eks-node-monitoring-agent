package collect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// na-cli binary names. The v4 and v6 builds decode only their own family's map
// keys. EC2 installs the family-correct build under the default name; Auto Mode
// bakes both (default = v4), so a published symlink disambiguates.
const (
	naCLIDefault = "aws-eks-na-cli"
	naCLIv6Named = "aws-eks-na-cli-v6"
)

// Bundle contract for the network-policy eBPF collector. These are load-bearing:
// tests (including e2e) key off them, so they are exported and referenced here
// rather than duplicated as literals.
const (
	// EBPFDataFile holds the loaded-ebpfdata output and the CLI selection line.
	EBPFDataFile = "networking/ebpf-data.txt"
	// EBPFMapsDataFile holds the per-map dumps; written only when maps exist.
	EBPFMapsDataFile = "networking/ebpf-maps-data.txt"
	// CLISelectionLinePrefix begins the line recording which binary (if any) was
	// chosen; always written once a binary is present.
	CLISelectionLinePrefix = "*** network-policy CLI selection:"
	// CLINotInstalledLinePrefix begins the line written when no na-cli is present.
	CLINotInstalledLinePrefix = "*** " + naCLIDefault + "/" + naCLIv6Named + " not present"
	// MapIDMarker begins each per-map dump section in EBPFMapsDataFile.
	MapIDMarker = "Map ID:"
	// CLIExecFailedMarker begins the line recorded when a chosen binary was run
	// but the exec itself failed (e.g. an SELinux denial on the Auto Mode managed
	// agent). It lets a reader distinguish a denied exec from an empty-map result.
	CLIExecFailedMarker = "failed to execute"
	// CLIExecFailedLinePrefix is the start of that artifact line; tests match the
	// whole prefix, so it is defined with the line rather than reassembled per caller.
	CLIExecFailedLinePrefix = "*** " + CLIExecFailedMarker
)

// networkPolicyCLIDirs are searched in order. The Auto Mode managed agent runs as
// SELinux system_t, which can exec /usr/bin (os_t) but not the /opt/cni/bin copy
// (cni_exec_t), so /usr/bin is tried first. On EC2 the agent runs as a privileged
// container domain that can exec either.
var networkPolicyCLIDirs = []string{"/usr/bin", "/opt/cni/bin"}

// naCLIPublishedDir is where the network policy agent publishes a symlink to the
// family-correct build at startup (it knows the family from its own config). In
// /run because /usr is immutable; absent until the agent starts, cleared on
// reboot. Absence means the family is unconfirmed and map decoding is skipped.
const naCLIPublishedDir = "/run/aws-network-policy-agent"

// findNetworkPolicyCLI returns the Root-prefixed path of the named CLI if it
// exists in one of networkPolicyCLIDirs.
func findNetworkPolicyCLI(acc *Accessor, name string) (string, bool) {
	for _, dir := range networkPolicyCLIDirs {
		p := filepath.Join(acc.cfg.Root, dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// findPublishedNetworkPolicyCLI returns the published symlink if it exists and
// its target exists, both resolved under acc.cfg.Root. The network policy agent
// writes an absolute target, so it is re-rooted under Root before the existence
// check rather than followed by os.Stat (which would resolve against the wrong
// namespace when Root != "/"). A dangling link reads as absent, so decoding is
// skipped.
func findPublishedNetworkPolicyCLI(acc *Accessor) (string, bool) {
	link := filepath.Join(acc.cfg.Root, naCLIPublishedDir, naCLIDefault)
	target, err := os.Readlink(link)
	if err != nil {
		return "", false
	}
	// Re-root an absolute target under Root; a relative target is already
	// relative to the link's directory.
	resolved := target
	if filepath.IsAbs(target) {
		resolved = filepath.Join(acc.cfg.Root, target)
	} else {
		resolved = filepath.Join(filepath.Dir(link), target)
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", false
	}
	return link, true
}

// chooseNetworkPolicyCLI is the pure selection decision, split from the filesystem
// side for unit testing. The published symlink is the only family signal; there is
// no family query and no decode probe. EC2 installs the family-correct build under
// the default name, so a single default-named binary is safe to run; two builds
// without a symlink (Auto Mode) leaves the family unconfirmed, so it skips.
func chooseNetworkPolicyCLI(publishedCLI string, havePublished bool, defaultCLI string, haveDefault, haveV6Named bool) (cliPath string, reason string, ok bool) {
	switch {
	case havePublished:
		return publishedCLI, "NPA-published family-correct symlink present", true
	case !haveDefault && !haveV6Named:
		return "", fmt.Sprintf("neither %s nor %s found in %v; network policy agent not installed", naCLIDefault, naCLIv6Named, networkPolicyCLIDirs), false
	case haveDefault && !haveV6Named:
		return defaultCLI, "single binary (aws-eks-na-cli) present — installed family-correct by the CNI daemonset", true
	}
	return "", "family unconfirmed (no NPA-published symlink), skipping map dumps", false
}

// networkPolicyEbpfInfo dumps the network-policy eBPF loaded programs and map
// contents via aws-eks-na-cli. The family is confirmed first (chooseNetworkPolicyCLI),
// and the confirmed binary runs BOTH loaded-ebpfdata and dump-maps. We do not
// rely on any subcommand being family-agnostic: if the family is unconfirmed, no
// na-cli command is run and only the selection reason is recorded.
func networkPolicyEbpfInfo(acc *Accessor) error {
	ebpfDataFile := EBPFDataFile

	publishedCLI, havePublished := findPublishedNetworkPolicyCLI(acc)
	defaultCLI, haveDefault := findNetworkPolicyCLI(acc, naCLIDefault)
	_, haveV6Named := findNetworkPolicyCLI(acc, naCLIv6Named)

	// No binary at all is distinct from "installed but family unconfirmed"; report it
	// plainly, via the shared prefix constant so the producer and the e2e can't drift.
	if !haveDefault && !haveV6Named {
		return acc.appendOutput(ebpfDataFile, []byte(CLINotInstalledLinePrefix+", skipping eBPF map collection ***\n"))
	}

	cliPath, reason, ok := chooseNetworkPolicyCLI(publishedCLI, havePublished, defaultCLI, haveDefault, haveV6Named)
	if err := acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("%s %s ***\n", CLISelectionLinePrefix, reason))); err != nil {
		return fmt.Errorf("failed to append CLI selection to %s: %w", ebpfDataFile, err)
	}
	if !ok {
		// Family unconfirmed: run no na-cli command rather than guess a family.
		return nil
	}

	if err := acc.appendOutput(ebpfDataFile, []byte("*** EBPF loaded data ***\n")); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}
	loaded, err := acc.Command(cliPath, "ebpf", "loaded-ebpfdata").CombinedOutput()
	if err != nil {
		// Record the failure under the header (e.g. a denied exec on the Auto Mode
		// managed agent) rather than only in the joined error.
		return errors.Join(
			acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("%s %s ebpf loaded-ebpfdata: %v ***\n", CLIExecFailedLinePrefix, cliPath, err))),
			fmt.Errorf("%s %s: %w", CLIExecFailedMarker, cliPath, err),
		)
	}
	if err := acc.appendOutput(ebpfDataFile, loaded); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}

	ebpfMapFile := EBPFMapsDataFile
	mapIDs := uniqueMapIDs(string(loaded))
	var merr error
	for _, mapID := range mapIDs {
		// Per-map dump is best-effort (IgnoreFailure): one undumpable map (e.g. GC'd
		// between enumerate and dump) must not fail the whole bundle capture.
		merr = errors.Join(merr,
			acc.appendOutput(ebpfMapFile, []byte(fmt.Sprintf("*** EBPF map data for %s %s ***\n", MapIDMarker, mapID))),
			acc.CommandOutput([]string{cliPath, "ebpf", "dump-maps", mapID}, ebpfMapFile, CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
		)
	}
	return merr
}

// uniqueMapIDs extracts the distinct "Map ID: <n>" values from loaded-ebpfdata
// output, sorted, so map collection is deterministic run-to-run.
func uniqueMapIDs(loaded string) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, line := range strings.Split(loaded, "\n") {
		idx := strings.Index(line, MapIDMarker)
		if idx < 0 {
			continue
		}
		id := strings.TrimSpace(line[idx+len(MapIDMarker):])
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
