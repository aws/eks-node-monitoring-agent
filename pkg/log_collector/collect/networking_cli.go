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

// The collector's output contract: the bundle file paths and the marker strings it
// writes, as named constants rather than inline literals so there is one definition.
const (
	// ebpfDataFile holds the loaded-ebpfdata output and the CLI selection line.
	ebpfDataFile = "networking/ebpf-data.txt"
	// ebpfMapsDataFile holds the per-map dumps; written only when maps exist.
	ebpfMapsDataFile = "networking/ebpf-maps-data.txt"
	// cliSelectionLinePrefix begins the line recording the CLI selection outcome:
	// which binary was chosen, or why none was. Always written.
	cliSelectionLinePrefix = "*** network-policy CLI selection:"
	// mapIDMarker begins each per-map dump section in ebpfMapsDataFile.
	mapIDMarker = "Map ID:"
	// cliExecFailedLinePrefix begins the line recorded when a chosen binary was run
	// but the exec itself failed (e.g. an SELinux denial on the Auto Mode managed
	// agent). It lets a reader distinguish a denied exec from an empty-map result.
	cliExecFailedLinePrefix = "*** failed to execute"
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
	var resolved string
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
// side for unit testing. The published symlink is the only family signal (no family
// query, no decode probe): a single installed binary is family-correct by the CNI
// daemonset; two builds without a symlink leave the family unconfirmed, so it skips.
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
	publishedCLI, havePublished := findPublishedNetworkPolicyCLI(acc)
	defaultCLI, haveDefault := findNetworkPolicyCLI(acc, naCLIDefault)
	_, haveV6Named := findNetworkPolicyCLI(acc, naCLIv6Named)

	// chooseNetworkPolicyCLI owns the whole decision, including "not installed",
	// so the selection line is the single record of what happened.
	cliPath, reason, ok := chooseNetworkPolicyCLI(publishedCLI, havePublished, defaultCLI, haveDefault, haveV6Named)
	if err := acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("%s %s ***\n", cliSelectionLinePrefix, reason))); err != nil {
		return fmt.Errorf("failed to append CLI selection to %s: %w", ebpfDataFile, err)
	}
	if !ok {
		// No binary, or family unconfirmed: run no na-cli command rather than guess.
		return nil
	}

	if err := acc.appendOutput(ebpfDataFile, []byte("*** EBPF loaded data ***\n")); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}
	loaded, err := acc.Command(cliPath, "ebpf", "loaded-ebpfdata").CombinedOutput()
	if err != nil {
		// Best-effort: record the failure line and return nil, so a denied exec (e.g.
		// on the Auto Mode managed agent) doesn't fail the whole capture. Same as the
		// per-map dumps below.
		return acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("%s %s ebpf loaded-ebpfdata: %v ***\n", cliExecFailedLinePrefix, cliPath, err)))
	}
	if err := acc.appendOutput(ebpfDataFile, loaded); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}

	mapIDs := uniqueMapIDs(string(loaded))
	var merr error
	for _, mapID := range mapIDs {
		// Per-map dump is best-effort (IgnoreFailure): one undumpable map (e.g. GC'd
		// between enumerate and dump) must not fail the whole bundle capture.
		merr = errors.Join(merr,
			acc.appendOutput(ebpfMapsDataFile, []byte(fmt.Sprintf("*** EBPF map data for %s %s ***\n", mapIDMarker, mapID))),
			acc.CommandOutput([]string{cliPath, "ebpf", "dump-maps", mapID}, ebpfMapsDataFile, CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
		)
	}
	return merr
}

// uniqueMapIDs extracts the distinct "Map ID: <n>" values from loaded-ebpfdata
// output, lexically sorted, so map collection is deterministic run-to-run.
func uniqueMapIDs(loaded string) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, line := range strings.Split(loaded, "\n") {
		idx := strings.Index(line, mapIDMarker)
		if idx < 0 {
			continue
		}
		id := strings.TrimSpace(line[idx+len(mapIDMarker):])
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
