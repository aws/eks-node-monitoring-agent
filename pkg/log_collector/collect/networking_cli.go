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

// Output contract: the bundle file paths and marker strings the collector writes.
const (
	// ebpfDataFile holds the loaded-ebpfdata output and the CLI selection line.
	ebpfDataFile = "networking/ebpf-data.txt"
	// ebpfMapsDataFile holds the ingress_map/egress_map dumps; written only when
	// such maps exist.
	ebpfMapsDataFile = "networking/ebpf-maps-data.txt"
	// ebpfCPMapsDataFile holds the cp_ingress_map/cp_egress_map (cluster policy)
	// dumps; written only when such maps exist.
	ebpfCPMapsDataFile = "networking/ebpf-cp-maps-data.txt"
	// cliSelectionLinePrefix begins the line recording the CLI selection outcome:
	// which binary was chosen, or why none was. Always written.
	cliSelectionLinePrefix = "*** network-policy CLI selection:"
	// mapIDMarker begins each per-map dump section in the maps data files, and the
	// ID line in loaded-ebpfdata output.
	mapIDMarker = "Map ID:"
	// mapNameMarker begins the name line that precedes a map's ID line in
	// loaded-ebpfdata output.
	mapNameMarker = "Map Name:"
	// mapDumpFailedLinePrefix begins the line written in place of a map's contents
	// when its dump command failed.
	mapDumpFailedLinePrefix = "*** failed to dump"
	// cliExecFailedLinePrefix begins the line written when the chosen binary could not
	// be executed (e.g. an SELinux denial on the Auto Mode agent), distinct from empty maps.
	cliExecFailedLinePrefix = "*** failed to execute"
)

// networkPolicyCLIDirs are searched in order. /usr/bin is first because the Auto Mode
// managed agent (SELinux system_t) can exec it but not the /opt/cni/bin copy (cni_exec_t);
// on EC2 the agent can exec either.
var networkPolicyCLIDirs = []string{"/usr/bin", "/opt/cni/bin"}

// naCLIPublishedDir is where the network policy agent publishes a symlink to the
// family-correct build at startup. Absence means the family is unconfirmed, so map
// decoding is skipped.
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

// findPublishedNetworkPolicyCLI returns the published symlink if both it and its
// target exist under acc.cfg.Root. The target is absolute, so it is re-rooted under
// Root before the check rather than os.Stat'd directly (which would resolve against
// the wrong namespace when Root != "/"). A dangling link reads as absent.
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

// chooseNetworkPolicyCLI is the pure selection decision. The published symlink is the
// only family signal: a single installed binary is family-correct by the CNI daemonset;
// two builds without a symlink leave the family unconfirmed, so it skips.
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

// networkPolicyEbpfInfo dumps the network-policy eBPF programs and map contents via
// aws-eks-na-cli. The family is confirmed first; if it is unconfirmed, no command is
// run and only the selection reason is recorded.
func networkPolicyEbpfInfo(acc *Accessor) error {
	publishedCLI, havePublished := findPublishedNetworkPolicyCLI(acc)
	defaultCLI, haveDefault := findNetworkPolicyCLI(acc, naCLIDefault)
	_, haveV6Named := findNetworkPolicyCLI(acc, naCLIv6Named)

	// chooseNetworkPolicyCLI owns the whole decision, so the selection line is the
	// single record of the outcome.
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
	loaded, err := acc.CombinedOutput(cliPath, "ebpf", "loaded-ebpfdata")
	if err != nil {
		// Best-effort: record the failure and return nil so a denied exec doesn't fail
		// the whole capture, like the per-map dumps below.
		return acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("%s %s ebpf loaded-ebpfdata: %v ***\n", cliExecFailedLinePrefix, cliPath, err)))
	}
	if err := acc.appendOutput(ebpfDataFile, loaded); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}

	maps, cpMaps := policyMapIDs(string(loaded))
	// Logged so that a parser that stops matching the CLI output (zero policy maps
	// out of many listed) is visible in the agent logs, not only as missing files.
	acc.logger.Info("network policy eBPF maps selected",
		"listedMapIDs", strings.Count(string(loaded), mapIDMarker),
		"dumpMaps", len(maps), "dumpCPMaps", len(cpMaps))
	return errors.Join(
		dumpMaps(acc, cliPath, "dump-maps", maps, ebpfMapsDataFile),
		dumpMaps(acc, cliPath, "dump-cp-maps", cpMaps, ebpfCPMapsDataFile),
	)
}

// dumpMaps dumps each map ID with the given na-cli subcommand into dest. The dump
// is best-effort: one undumpable map (e.g. GC'd between enumerate and dump) must
// not fail the whole bundle capture, but the failure is recorded in dest so it
// is visible rather than silently missing.
func dumpMaps(acc *Accessor, cliPath, subcommand string, mapIDs []string, dest string) error {
	var merr error
	for _, mapID := range mapIDs {
		merr = errors.Join(merr, acc.appendOutput(dest, []byte(fmt.Sprintf("*** EBPF map data for %s %s ***\n", mapIDMarker, mapID))))
		if err := acc.CommandOutput([]string{cliPath, "ebpf", subcommand, mapID}, dest, CommandOptionsAppend|CommandOptionsNoStderr); err != nil {
			acc.logger.Info("map dump failed", "mapID", mapID, "subcommand", subcommand, "error", err.Error())
			merr = errors.Join(merr, acc.appendOutput(dest, []byte(fmt.Sprintf("%s %s %s: %v ***\n", mapDumpFailedLinePrefix, mapIDMarker, mapID, err))))
		}
	}
	return merr
}

// policyMapIDs extracts, from loaded-ebpfdata output, the distinct IDs of the
// maps that hold network policy, each lexically sorted so collection is
// deterministic run-to-run. ingress_map/egress_map IDs are returned in maps and
// cp_ingress_map/cp_egress_map IDs in cpMaps, because the two kinds decode with
// different na-cli subcommands. Every other map is ignored.
//
// A "Map Name:" line names the "Map ID:" line that follows it.
func policyMapIDs(loaded string) (maps, cpMaps []string) {
	seen := make(map[string]bool)
	var name string
	for _, line := range strings.Split(loaded, "\n") {
		if idx := strings.Index(line, mapNameMarker); idx >= 0 {
			name = strings.TrimSpace(line[idx+len(mapNameMarker):])
			continue
		}
		idx := strings.Index(line, mapIDMarker)
		if idx < 0 {
			continue
		}
		id := strings.TrimSpace(line[idx+len(mapIDMarker):])
		mapName := name
		name = ""
		if id == "" || seen[id] {
			continue
		}
		switch mapName {
		case "ingress_map", "egress_map":
			maps = append(maps, id)
		case "cp_ingress_map", "cp_egress_map":
			cpMaps = append(cpMaps, id)
		default:
			continue
		}
		seen[id] = true
	}
	sort.Strings(maps)
	sort.Strings(cpMaps)
	return maps, cpMaps
}
