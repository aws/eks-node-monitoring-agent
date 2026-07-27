package collect

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/aws/eks-node-monitoring-agent/pkg/pathlib"
	netutils "github.com/aws/eks-node-monitoring-agent/pkg/util/net"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/networkutils"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/validation"
)

type Networking struct {
}

func (m Networking) Collect(acc *Accessor) error {
	return errors.Join(
		multicard(acc),
		resolv(acc),
		ping(acc),
		conntrack(acc),
		interfaces(acc),
		ipInfo(acc),
		apiServerConnectivity(acc),
		systemdNetworkConfig(acc),
		networkPolicyEbpfInfo(acc),
	)
}

func conntrack(acc *Accessor) error {
	return errors.Join(
		acc.appendOutput("networking/conntrack.txt", []byte("*** Output of conntrack -S ***\n")),
		acc.CommandOutput([]string{"conntrack", "-S"}, "networking/conntrack.txt", CommandOptionsAppend|CommandOptionsNoStderr),
		acc.appendOutput("networking/conntrack.txt", []byte("*** Output of conntrack -L ***\n")),
		acc.CommandOutput([]string{"conntrack", "-L"}, "networking/conntrack.txt", CommandOptionsAppend|CommandOptionsNoStderr),
		acc.appendOutput("networking/conntrack6.txt", []byte("*** Output of conntrack -L -f ipv6 ***\n")),
		acc.CommandOutput([]string{"conntrack", "-L", "-f", "ipv6"}, "networking/conntrack6.txt", CommandOptionsAppend|CommandOptionsNoStderr),
	)
}

func ipInfo(acc *Accessor) error {
	var merr error
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		merr = errors.Join(merr, acc.CommandOutput([]string{"ifconfig"}, "networking/ifconfig.txt", CommandOptionsNone))
	}
	return errors.Join(merr,
		acc.CommandOutput([]string{"ip", "rule", "show"}, "networking/iprule.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ip", "-6", "rule", "show"}, "networking/ip6rule.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ip", "route", "show", "table", "all"}, "networking/iproute.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ip", "-6", "route", "show", "table", "all"}, "networking/ip6route.txt", CommandOptionsNone),
	)
}

func multicard(acc *Accessor) error {
	return acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "configure-multicard-interfaces"}, "networking/configure-multicard-interfaces.txt", CommandOptionsNone)
}

func interfaces(acc *Accessor) error {
	netInterfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	var merr error
	for _, netInterface := range netInterfaces {
		output, err := acc.Command("ethtool", "-S", netInterface.Name).CombinedOutput()
		// we can still use the output of ethtool if there is an error.
		if err != nil && len(output) == 0 {
			merr = errors.Join(merr, err)
			continue
		}
		merr = errors.Join(merr,
			acc.appendOutput("networking/ethtool.txt", []byte("Interface "+netInterface.Name+"\n")),
			acc.appendOutput("networking/ethtool.txt", output),
			acc.appendOutput("networking/ethtool.txt", []byte("\n")),
		)
	}
	return merr
}

func resolv(acc *Accessor) error {
	return acc.CopyFile(filepath.Join(acc.cfg.Root, "/etc/resolv.conf"), "networking/resolv.conf")
}

func ping(acc *Accessor) error {
	var merr error
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		// TODO: we can move off of calling ping binary and use an ICMP library
		merr = errors.Join(merr,
			acc.CommandOutput([]string{"ping", "-A", "-c", "10", "www.amazon.com"}, "networking/ping_amazon.com.txt", CommandOptionsNone),
			acc.CommandOutput([]string{"ping", "-A", "-c", "10", "public.ecr.aws"}, "networking/ping_public.ecr.aws.txt", CommandOptionsNone),
		)
	}
	return merr
}

func systemdNetworkConfig(acc *Accessor) error {
	// Get active networkd interfaces using networkctl with osext.NewExec
	interfaces, err := networkutils.GetNetworkInterfaces(acc)
	if err != nil {
		// If we have an error getting interfaces, log it
		return acc.appendOutput("networking/CheckMacAddressPolicy.log",
			[]byte(fmt.Sprintf("Failed to get network interfaces: %v\n", err)))
	}

	// Make sure the systemd-network directory exists
	if err := os.MkdirAll(filepath.Join(acc.cfg.Destination, "networking/systemd-network"), 0755); err != nil {
		return err
	}

	// Deduplicate by LinkFile to avoid processing the same file multiple times
	processedLinkFiles := make(map[string]bool)
	var merr error

	// Process each interface with a LinkFile
	for _, iface := range interfaces {
		if iface.LinkFile == "" || processedLinkFiles[iface.LinkFile] {
			continue
		}
		processedLinkFiles[iface.LinkFile] = true

		// Use LinkFile basename as filename to avoid path issues
		safeFileName := strings.ReplaceAll(filepath.Base(iface.LinkFile), "/", "_")
		outputPath := filepath.Join("networking/systemd-network", safeFileName)

		// Just dump the entire output to the file
		merr = errors.Join(merr,
			acc.CommandOutput([]string{"systemd-analyze", "cat-config", iface.LinkFile},
				outputPath, CommandOptionsIgnoreFailure))
	}
	return merr
}

func apiServerConnectivity(acc *Accessor) error {
	kubeconfigPath := pathlib.ResolveKubeconfig(acc.cfg.Root)
	if len(kubeconfigPath) == 0 {
		return fmt.Errorf("could not find kubeconfig")
	}
	// builds a config from kubeconfig path
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to build kubernetes config from kubeconfig: %w", err)
	}

	var merr error
	for _, cluster := range config.Clusters {
		apiServerUrl, err := validation.ParseAPIServerURL(cluster.Server)
		if err != nil {
			return fmt.Errorf("failed to parse server url: %w", err)
		}
		apiServerUrl.Path = "/livez"
		apiServerUrl.RawQuery = "verbose"
		livezRequest, err := http.NewRequest(http.MethodGet, apiServerUrl.String(), nil)
		if err != nil {
			return fmt.Errorf("failed to build request: %w", err)
		}
		caData := cluster.CertificateAuthorityData
		if len(caData) == 0 {
			caCertPath := cluster.CertificateAuthority
			// fixup the path if it comes from the host machine
			if acc.cfg.Root != "/" && !strings.HasPrefix(caCertPath, acc.cfg.Root) {
				caCertPath = filepath.Join(acc.cfg.Root, caCertPath)
			}
			caBytes, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("failed to read caCert: %w", err)
			}
			caData = caBytes
		}

		merr = errors.Join(merr, acc.appendOutput("networking/get_api_server.txt", []byte(fmt.Sprintf("sending GET request to %s\n", apiServerUrl.String()))))
		if body, err := netutils.DoRequest(livezRequest, netutils.WithCaCert(caData)); err != nil {
			merr = errors.Join(merr, fmt.Errorf("failed to make api server request: %w", err))
		} else {
			defer body.Close()
			if data, err := io.ReadAll(body); err != nil {
				merr = errors.Join(merr, fmt.Errorf("failed to read api server response: %w", err))
			} else {
				// we cant accurately represent curl from the following line, so
				// this is being written to a different path than 'networking/curl_api_server.txt'
				// https://github.com/awslabs/amazon-eks-ami/blob/dd41db152bbaa3f86ad5b577891c77c14af2ed33/log-collector-script/linux/eks-log-collector.sh#L592
				merr = errors.Join(merr, acc.appendOutput("networking/get_api_server.txt", data))
			}
		}
	}
	return merr
}

// na-cli binary names. The v4 and v6 builds decode only their own family's map
// keys. EC2 installs the family-correct build under the default name; Auto Mode
// bakes both (default = v4), so a published symlink disambiguates.
const (
	naCLIDefault = "aws-eks-na-cli"
	naCLIv6Named = "aws-eks-na-cli-v6"
)

// networkPolicyCLIDirs are searched in order. On Bottlerocket the /opt/cni/bin
// copy is SELinux-labeled cni_exec_t, which the agent's domain (system_t) cannot
// execute; the /usr/bin copy is os_t, which it can. So /usr/bin is tried first,
// with /opt/cni/bin as a fallback for images that predate the /usr/bin install.
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
// its target exists, both resolved under acc.cfg.Root. The link target is an
// absolute path, so it is re-rooted under Root before the existence check rather
// than followed by os.Stat (which would resolve against the wrong namespace when
// Root != "/"). A dangling link reads as absent, so decoding is skipped.
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

// selectNetworkPolicyCLI chooses which na-cli binary may decode eBPF maps.
// Returns ok=false to skip decoding when no CLI is installed or the family is
// unconfirmed. loaded-ebpfdata (family-agnostic) is collected regardless.
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
		return defaultCLI, "single binary (aws-eks-na-cli) present — installed family-correct by the CNI daemonset: using it", true
	case !haveDefault && haveV6Named:
		return v6NamedCLI, fmt.Sprintf("only %s present (unexpected layout); using it", naCLIv6Named), true
	}
	return "", "both na-cli builds present but no NPA-published symlink; family unconfirmed, skipping map dumps", false
}

// networkPolicyEbpfInfo dumps the network-policy eBPF loaded programs and map
// contents via aws-eks-na-cli. loaded-ebpfdata is family-agnostic and runs with
// whichever binary is installed; the per-map dump binary is chosen by
// selectNetworkPolicyCLI.
func networkPolicyEbpfInfo(acc *Accessor) error {
	ebpfDataFile := "networking/ebpf-data.txt"

	listCLI, listOK := findNetworkPolicyCLI(acc, naCLIDefault)
	if !listOK {
		listCLI, listOK = findNetworkPolicyCLI(acc, naCLIv6Named)
	}
	if !listOK {
		return acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("*** %s/%s not present, skipping eBPF map collection ***\n", naCLIDefault, naCLIv6Named)))
	}

	if err := acc.appendOutput(ebpfDataFile, []byte("*** EBPF loaded data ***\n")); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}
	loaded, err := acc.Command(listCLI, "ebpf", "loaded-ebpfdata").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to execute %s: %w", listCLI, err)
	}
	if err := acc.appendOutput(ebpfDataFile, loaded); err != nil {
		return fmt.Errorf("failed to append output to %s: %w", ebpfDataFile, err)
	}

	// Record the selection reason even on skip: it is the bundle's diagnostic.
	cliPath, reason, ok := selectNetworkPolicyCLI(acc)
	if err := acc.appendOutput(ebpfDataFile, []byte(fmt.Sprintf("*** network-policy CLI selection: %s ***\n", reason))); err != nil {
		return fmt.Errorf("failed to append CLI selection to %s: %w", ebpfDataFile, err)
	}
	if !ok {
		return nil
	}

	ebpfMapFile := "networking/ebpf-maps-data.txt"
	mapIDs := uniqueMapIDs(string(loaded))
	var merr error
	for _, mapID := range mapIDs {
		merr = errors.Join(merr,
			acc.appendOutput(ebpfMapFile, []byte(fmt.Sprintf("*** EBPF map data for Map ID: %s ***\n", mapID))),
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
		idx := strings.Index(line, "Map ID:")
		if idx < 0 {
			continue
		}
		id := strings.TrimSpace(line[idx+len("Map ID:"):])
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
