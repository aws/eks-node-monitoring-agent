package pathlib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const DefaultCACertPath = "/etc/kubernetes/pki/ca.crt"

func ResolveCACertPath(hostRoot string) string {
	return ResolvePathOption(hostRoot, filepath.Join(hostRoot, DefaultCACertPath))
}

// ResolveClusterCACert determines the CA certificate a kubeconfig cluster expects.
// It returns an empty path when the cluster embeds certificate-authority-data
// (which is self-contained and needs no file), the host-root-prefixed path when
// the cluster references a CA file, and the well-known host location as a last
// resort.
func ResolveClusterCACert(hostRoot string, cluster *clientcmdapi.Cluster) (string, error) {
	if cluster != nil {
		if len(cluster.CertificateAuthorityData) > 0 {
			return "", nil
		}
		if caCertPath := cluster.CertificateAuthority; caCertPath != "" {
			return HostPath(hostRoot, caCertPath), nil
		}
	}

	if caCertPath := ResolveCACertPath(hostRoot); caCertPath != "" {
		return caCertPath, nil
	}
	return "", fmt.Errorf("could not locate host CA Certificates in expected paths")
}

// HostPath prefixes a host-absolute path with the host root mount, so a path a
// kubeconfig references can be opened from inside the container. Empty paths and
// paths already pointing inside the mount are returned unchanged. The
// inside-the-mount check compares whole path components, so a sibling like
// /hostname/pki/ca.crt is still prefixed when the mount is /host.
func HostPath(hostRoot, path string) string {
	hostRoot = filepath.Clean(hostRoot)
	insideHostRoot := path == hostRoot || strings.HasPrefix(path, hostRoot+"/")
	if path == "" || hostRoot == "/" || insideHostRoot {
		return path
	}
	return filepath.Join(hostRoot, path)
}

// AuthInfoForCurrentContext returns the user referenced by the kubeconfig's
// current context, falling back to the sole user when unambiguous.
func AuthInfoForCurrentContext(kubeconfig *clientcmdapi.Config) *clientcmdapi.AuthInfo {
	if ctx, ok := kubeconfig.Contexts[kubeconfig.CurrentContext]; ok {
		if authInfo, ok := kubeconfig.AuthInfos[ctx.AuthInfo]; ok {
			return authInfo
		}
	}
	if len(kubeconfig.AuthInfos) == 1 {
		for _, authInfo := range kubeconfig.AuthInfos {
			return authInfo
		}
	}
	return nil
}

// ClusterForCurrentContext returns the cluster referenced by the kubeconfig's
// current context, falling back to the sole cluster when unambiguous.
func ClusterForCurrentContext(kubeconfig *clientcmdapi.Config) *clientcmdapi.Cluster {
	if ctx, ok := kubeconfig.Contexts[kubeconfig.CurrentContext]; ok {
		if cluster, ok := kubeconfig.Clusters[ctx.Cluster]; ok {
			return cluster
		}
	}
	if len(kubeconfig.Clusters) == 1 {
		for _, cluster := range kubeconfig.Clusters {
			return cluster
		}
	}
	return nil
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
