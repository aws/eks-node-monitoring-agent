package collect

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
		output, err := acc.CombinedOutput("ethtool", "-S", netInterface.Name)
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
	var merr error
	dumped := make(map[string]bool)

	// Get active networkd interfaces using networkctl with osext.NewExec
	interfaces, err := networkutils.GetNetworkInterfaces(acc)
	if err != nil {
		// If we have an error getting interfaces, log it and carry on
		merr = errors.Join(merr, acc.appendOutput("networking/CheckMacAddressPolicy.log",
			[]byte(fmt.Sprintf("Failed to get network interfaces: %v\n", err))))
	}

	// Make sure the systemd-network directory exists
	if err := os.MkdirAll(filepath.Join(acc.cfg.Destination, "networking/systemd-network"), 0755); err != nil {
		return errors.Join(merr, err)
	}

	// Deduplicate by LinkFile to avoid processing the same file multiple times
	processedLinkFiles := make(map[string]bool)

	// Process each interface with a LinkFile
	for _, iface := range interfaces {
		if iface.LinkFile == "" || processedLinkFiles[iface.LinkFile] {
			continue
		}
		processedLinkFiles[iface.LinkFile] = true

		// Use LinkFile basename as filename to avoid path issues
		safeFileName := strings.ReplaceAll(filepath.Base(iface.LinkFile), "/", "_")
		outputPath := filepath.Join("networking/systemd-network", safeFileName)
		dumped[safeFileName] = true

		// Just dump the entire output to the file
		merr = errors.Join(merr,
			acc.CommandOutput([]string{"systemd-analyze", "cat-config", iface.LinkFile},
				outputPath, CommandOptionsIgnoreFailure))
	}

	// Also dump the units that no interface is using
	units, err := networkdUnitFiles(acc)
	merr = errors.Join(merr, err)
	return errors.Join(merr, dumpNetworkdUnits(acc, units, dumped))
}

// networkdConfigDirs are the directories systemd-networkd reads units from, in
// precedence order. /lib only differs from /usr/lib where /usr isn't merged.
// see: https://www.freedesktop.org/software/systemd/man/latest/systemd.network.html
var networkdConfigDirs = []string{
	"/etc/systemd/network",
	"/run/systemd/network",
	"/usr/local/lib/systemd/network",
	"/usr/lib/systemd/network",
	"/lib/systemd/network",
}

// networkdUnitSuffixes are the unit types systemd-networkd loads.
var networkdUnitSuffixes = []string{".link", ".network", ".netdev"}

// networkdUnitFiles lists the networkd units in networkdConfigDirs, in
// precedence order, as host paths. Symlinks are listed, not followed: an
// absolute target would resolve against the agent's filesystem, not the host's.
func networkdUnitFiles(acc *Accessor) ([]string, error) {
	var (
		merr  error
		units []string
	)
	for _, dir := range networkdConfigDirs {
		entries, err := os.ReadDir(filepath.Join(acc.cfg.Root, dir))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				merr = errors.Join(merr, err)
			}
			continue
		}
		for _, entry := range entries {
			// drop-in directories are left to systemd-analyze
			if !entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0 {
				continue
			}
			if !slices.ContainsFunc(networkdUnitSuffixes, func(suffix string) bool {
				return strings.HasSuffix(entry.Name(), suffix)
			}) {
				continue
			}
			units = append(units, filepath.Join(dir, entry.Name()))
		}
	}
	return units, merr
}

// dumpNetworkdUnits dumps the units not already in dumped. units are in
// precedence order, so only the copy systemd uses is dumped.
func dumpNetworkdUnits(acc *Accessor, units []string, dumped map[string]bool) error {
	var merr error
	for _, unit := range units {
		name := filepath.Base(unit)
		if dumped[name] {
			continue
		}
		dumped[name] = true
		// best-effort: one unreadable unit must not fail the capture
		merr = errors.Join(merr,
			acc.CommandOutput([]string{"systemd-analyze", "cat-config", unit},
				filepath.Join("networking/systemd-network", name), CommandOptionsIgnoreFailure))
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
			caCertPath, err := pathlib.ResolveClusterCACert(acc.cfg.Root, cluster)
			if err != nil {
				return err
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
