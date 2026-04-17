package collect

import (
	"errors"
)

type AutoMode struct{}

var _ Collector = (*AutoMode)(nil)

func (c *AutoMode) Collect(acc *Accessor) error {
	if !acc.cfg.hasAnyTag(TagEKSAuto) {
		// skip collection for non-Auto nodes which do not have below components running via systemd
		return nil
	}
	return errors.Join(
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-healthchecker"}, "automode/eks-healthchecker.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "kube-proxy"}, "automode/kube-proxy.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "coredns-bootstrap"}, "automode/coredns-boostrap.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "coredns"}, "automode/coredns.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-ebs-csi-driver"}, "automode/eks-ebs-csi-driver.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-node-monitoring-agent"}, "automode/eks-node-monitoring-agent.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "eks-pod-identity-agent"}, "automode/eks-pod-identity-agent.txt", CommandOptionsNone),
	)
}
