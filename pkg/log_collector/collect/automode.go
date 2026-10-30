package collect

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type AutoMode struct{}

var _ Collector = (*AutoMode)(nil)

func (c *AutoMode) Collect(acc *Accessor) error {
	if !acc.cfg.hasAnyTag(TagEKSAuto) {
		// skip collection for non-Auto nodes which do not have below components running via systemd
		return nil
	}
	var merr error
	if resp, err := collectEndpoint("http://127.0.0.1:62346/", "metrics"); err != nil {
		merr = fmt.Errorf("collecting eks-healthchecker metrics, %w", err)
	} else {
		merr = acc.WriteOutput("automode/eks-healthchecker-metrics.txt", filterMetrics(resp, healthcheckerMetricPrefix))
	}
	return errors.Join(
		merr,
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-healthchecker"}, "automode/eks-healthchecker.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "kube-proxy"}, "automode/kube-proxy.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "coredns-bootstrap"}, "automode/coredns-boostrap.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "coredns"}, "automode/coredns.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-ebs-csi-driver"}, "automode/eks-ebs-csi-driver.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-node-monitoring-agent"}, "automode/eks-node-monitoring-agent.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-networking-config-agent"}, "automode/eks-networking-config-agent.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-pod-identity-agent"}, "automode/eks-pod-identity-agent.txt", CommandOptionsNone),
	)
}

// healthcheckerMetricPrefix selects the eks-healthchecker's own metrics, dropping
// the Go runtime and process metrics the endpoint also exposes.
const healthcheckerMetricPrefix = "eks_healthcheck"

// filterMetrics keeps only the samples, and their HELP and TYPE comments, of
// the Prometheus text-format metrics whose name starts with prefix.
func filterMetrics(metrics []byte, prefix string) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(metrics), "\n") {
		name := line
		if comment, ok := strings.CutPrefix(line, "# "); ok {
			// "# HELP <name> ..." and "# TYPE <name> ..."
			if fields := strings.Fields(comment); len(fields) >= 2 {
				name = fields[1]
			}
		}
		if strings.HasPrefix(name, prefix) {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}
