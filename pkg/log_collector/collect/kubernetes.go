package collect

import (
	"errors"
	"fmt"

	"golang.a2z.com/Eks-node-monitoring-agent/pkg/pathlib"
)

type Kubernetes struct{}

func (m Kubernetes) Collect(acc *Accessor) error {
	return errors.Join(
		kubelet(acc),
		kubeconfig(acc),
	)
}

func kubelet(acc *Accessor) error {
	var merr = acc.CommandOutput([]string{"journalctl", "-u", "kubelet", "--since", "10 days ago"}, "kubelet/kubelet.log", CommandOptionsNone)

	// TODO: determine the source of the 'Access Denied' error that returns when
	// calling systemctl.
	if !acc.cfg.hasAnyTag(TagBottlerocket) || acc.cfg.hasAnyTag(TagEKSAuto) {
		merr = errors.Join(merr, acc.CommandOutput([]string{"systemctl", "cat", "kubelet"}, "kubelet/kubelet_service.txt", CommandOptionsNone))
	}

	if kubeletConfigPath := pathlib.ResolveKubeletConfig(acc.cfg.Root); len(kubeletConfigPath) > 0 {
		// NOTE: these aren't always json, but doing it for backwards
		// compatibility with the log analyzer
		merr = errors.Join(merr, acc.CopyFile(kubeletConfigPath, "kubelet/config.json"))
	}
	// kubelet supports drop in configs starting in 1.29
	if kubeletConfigDropinPath := pathlib.ResolveKubeletConfigDropIn(acc.cfg.Root); len(kubeletConfigDropinPath) > 0 {
		merr = errors.Join(merr, acc.CopyDir(kubeletConfigDropinPath, "kubelet/config.json.d"))
	}

	return merr
}

func kubeconfig(acc *Accessor) error {
	kubeconfigPath := pathlib.ResolveKubeconfig(acc.cfg.Root)
	if len(kubeconfigPath) == 0 {
		return fmt.Errorf("could not find kubeconfig")
	}
	return acc.CopyFile(kubeconfigPath, "kubelet/kubeconfig.yaml")
}
