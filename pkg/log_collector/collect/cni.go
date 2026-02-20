package collect

import (
	"errors"
	"path/filepath"
	"strings"
)

var _ Collector = (*CNI)(nil)

type CNI struct{}

func (c CNI) Collect(acc *Accessor) error {
	return errors.Join(
		cniConfig(acc),
		cniVariables(acc),
	)
}

func cniConfig(acc *Accessor) error {
	return acc.CopyDir(filepath.Join(acc.cfg.Root, "/etc/cni/net.d"), "cni")
}

func cniVariables(acc *Accessor) error {
	listOutput, err := acc.Command("ctr", "--namespace", "k8s.io", "container", "list").CombinedOutput()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(listOutput), "\n") {
		if strings.Contains(line, "amazon-k8s-cni:v") {
			containerId := strings.Split(line, " ")[0]
			return acc.CommandOutput([]string{"ctr", "--namespace", "k8s.io", "container", "info", containerId}, "cni/cni-configuration-variables-containerd.json", CommandOptionsNone)
		}
	}
	return nil
}
