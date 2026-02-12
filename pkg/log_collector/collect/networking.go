package collect

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"golang.a2z.com/Eks-node-monitoring-agent/pkg/pathlib"
	netutils "golang.a2z.com/Eks-node-monitoring-agent/pkg/util/net"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util/networkutils"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util/validation"
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
	return acc.CommandOutput([]string{"journalctl", "-u", "configure-multicard-interfaces"}, "networking/configure-multicard-interfaces.txt", CommandOptionsNone)
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
