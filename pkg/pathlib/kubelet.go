package pathlib

import (
	"os"
	"path/filepath"
)

const DefaultCACertPath = "/etc/kubernetes/pki/ca.crt"

func ResolveCACertPath(hostRoot string) string {
	return ResolvePathOption(hostRoot, filepath.Join(hostRoot, DefaultCACertPath))
}

func ResolveKubeletConfigDropIn(hostRoot string) string {
	return ResolveDirPathOption(hostRoot, filepath.Join(hostRoot, "/etc/kubernetes/kubelet/config.json.d"))
}

func ResolveKubeletConfig(hostRoot string) string {
	return ResolvePathOption(hostRoot,
		filepath.Join(hostRoot, "/etc/kubernetes/kubelet/config"), // bottlerocket
		filepath.Join(hostRoot, "/etc/kubernetes/kubelet/config.json"),
		filepath.Join(hostRoot, "/etc/kubernetes/kubelet/config.yaml"),
		filepath.Join(hostRoot, "/etc/kubernetes/kubelet/kubelet-config.json"), // eks al2 bootstrap
	)
}

func ResolveKubeconfig(hostRoot string) string {
	return ResolvePathOption(hostRoot,
		filepath.Join(hostRoot, os.ExpandEnv("${KUBECONFIG}")),
		filepath.Join(hostRoot, "/var/lib/kubelet/kubeconfig"),        // eks bootstrap
		filepath.Join(hostRoot, "/etc/kubernetes/kubelet/kubeconfig"), // bottlerocket
		filepath.Join(hostRoot, "/etc/eksctl/kubeconfig.yaml"),        // eksctl
	)
}

func ResolveDirPathOption(hostRoot string, options ...string) string {
	for _, path := range options {
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return path
		}
	}
	return ""
}

func ResolvePathOption(hostRoot string, options ...string) string {
	for _, path := range options {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}
