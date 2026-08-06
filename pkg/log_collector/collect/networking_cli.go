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
	// CLISelectedMarker appears in a selection reason when a binary was chosen
	// (as opposed to a skip). Selection reasons must contain it; skip reasons
	// must not.
	CLISelectedMarker = "using it"
	// MapIDMarker begins each per-map dump section in EBPFMapsDataFile.
	MapIDMarker = "Map ID:"
)

// networkPolicyCLIDirs are searched in order. On Bottlerocket the /opt/cni/bin
// copy is SELinux-labeled cni_exec_t, which the agent's domain (system_t) cannot
// execute; the /usr/bin copy is os_t, which it can, so /usr/bin is tried first.
// /opt/cni/bin is the steady-state location of the CNI daemonset install on EC2
// AL2023, where SELinux does not deny the exec.
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

// selectNetworkPolicyCLI chooses the family-confirmed na-cli binary to run.
// Returns ok=false when no CLI is installed or the family is unconfirmed, in
// which case no na-cli command is run at all.
func selectNetworkPolicyCLI(acc *Accessor) (cliPath string, reason string, ok bool) {
	publishedCLI, havePublished := findPublishedNetworkPolicyCLI(acc)
	defaultCLI, haveDefault := findNetworkPolicyCLI(acc, naCLIDefault)
	v6NamedCLI, haveV6Named := findNetworkPolicyCLI(acc, naCLIv6Named)
	return chooseNetworkPolicyCLI(publishedCLI, havePublished, defaultCLI, haveDefault, v6NamedCLI, haveV6Named)
}

// chooseNetworkPolicyCLI is the pure selection decision, split from the
// filesystem side for unit testing. The published symlink is the only family
// signal; there is no family query and no decode probe.
func chooseNetworkPolicyCLI(publishedCLI string, havePublished bool, defaultCLI string, haveDefault bool, v6NamedCLI string, haveV6Named bool) (cliPath string, reason string, ok bool) {
	switch {
	case havePublished:
		return publishedCLI, "NPA-published family-correct symlink present: using it", true
	case !haveDefault && !haveV6Named:
		return "", fmt.Sprintf("neither %s nor %s found in %v; network policy agent not installed", naCLIDefault, naCLIv6Named, networkPolicyCLIDirs), false
	case haveDefault && !haveV6Named:
		// A single installed binary is family-correct by the CNI daemonset, which
		// satisfies the "exec only when the family is confirmed" obligation. On EC2
		// Bottlerocket this selects the /opt/cni/bin copy and the exec is denied by
		// SELinux; that configuration is an open question in the design and the
		// denial now surfaces in the bundle text.
		return defaultCLI, "single binary (aws-eks-na-cli) present — installed family-correct by the CNI daemonset: using it", true
	case !haveDefault && haveV6Named:
		// A lone v6-named build is also a single installed binary whose name
		// confirms its family, satisfying the same obligation.
		return v6NamedCLI, fmt.Sprintf("only %s present (unexpected layout); using it", naCLIv6Named), true
	}
	return "", "both na-cli builds present but no NPA-published symlink; family unconfirmed, skipping map dumps", false
}

// networkPolicyEbpfInfo dumps the network-policy eBPF loaded programs and map
// contents via aws-eks-na-cli. The family is confirmed first (selectNetworkPolicyCLI),
// and the confirmed binary runs BOTH loaded-ebpfdata and dump-maps. We do not
// rely on any subcommand being family-agnostic: if the family is unconfirmed, no
// na-cli command is run and only the selection reason is recorded.
func networkPolicyEbpfInfo(acc *Accessor) error {
	ebpfDataFile := EBPFDataFile

	// No binary at all is distinct from "installed but family unconfirmed"; report it plainly.
	_, haveDefault := findNetworkPolicyCLI(acc, naCLIDefault)
	_, haveV6Named := findNetworkPolicyCLI(acc, naCLIv6Named)
	if !haveDefault && !haveV6Named {
		return acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("*** %s/%s not present, skipping eBPF map collection ***\n", naCLIDefault, naCLIv6Named)))
	}

	cliPath, reason, ok := selectNetworkPolicyCLI(acc)
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
		// Keep the artifact self-contained: record the failure under the header
		// (a denied exec on Bottlerocket surfaces here) rather than only in the
		// joined error.
		return errors.Join(
			acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("*** failed to execute %s ebpf loaded-ebpfdata: %v ***\n", cliPath, err))),
			fmt.Errorf("failed to execute %s: %w", cliPath, err),
		)
	}
	if err := acc.appendOutput(ebpfDataFile, loaded); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}

	ebpfMapFile := EBPFMapsDataFile
	mapIDs := uniqueMapIDs(string(loaded))
	var merr error
	for _, mapID := range mapIDs {
		merr = errors.Join(merr,
			acc.appendOutput(ebpfMapFile, []byte(fmt.Sprintf("*** EBPF map data for %s %s ***\n", MapIDMarker, mapID))),
			acc.CommandOutput([]string{cliPath, "ebpf", "dump-maps", mapID}, ebpfMapFile, CommandOptionsAppend|CommandOptionsNoStderr),
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
