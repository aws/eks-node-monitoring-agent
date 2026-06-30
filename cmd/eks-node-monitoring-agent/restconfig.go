package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/pathlib"
)

func NewAutoRestConfigProvider(baseConfig *rest.Config) *autoRestConfigProvider {
	return &autoRestConfigProvider{
		baseConfig: baseConfig,
	}
}

type autoRestConfigProvider struct {
	baseConfig *rest.Config
}

func (rcp *autoRestConfigProvider) Provide() (*rest.Config, error) {
	restConfig := rest.CopyConfig(rcp.baseConfig)
	// if the environment is EKS auto we are using the kubelet profile, but
	// we dont want to inherit the impersonated rules so that we can still
	// patch the node status and events.
	restConfig.Impersonate = rest.ImpersonationConfig{}
	return restConfig, nil
}

func NewPodRestConfigProvider() *podRestConfigProvider {
	return &podRestConfigProvider{
		execMapper: chrootMapper{},
	}
}

type podRestConfigProvider struct {
	execMapper chrootMapper
}

func (rcp *podRestConfigProvider) Provide() (*rest.Config, error) {
	kubeconfigPath := pathlib.ResolveKubeconfig(config.HostRoot())
	if kubeconfigPath == "" {
		return nil, fmt.Errorf("could not locate host kubeconfig in expected paths")
	}

	// the kubeconfig references its TLS material (CA, client certificate and
	// key) by file path relative to the host, but the agent reads the host
	// filesystem through a mount. build overrides that rewrite those paths so
	// clientcmd can open them.
	overrides, err := rcp.hostPathOverrides(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	// attempt to pick up kubelet's cluster config from the node.
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}

	// transform the exec provider arugments, because it needs to call host
	// binaries in order to get a token from the iam authenticator.
	// NOTE: reminder that these calls also utilize hostNetworking in order to
	// reach IMDS to get host credentials.
	if restConfig.ExecProvider.Command, restConfig.ExecProvider.Args, err = rcp.execMapper.Map(
		restConfig.ExecProvider.Command,
		restConfig.ExecProvider.Args...,
	); err != nil {
		return nil, err
	}

	return restConfig, nil
}

// hostPathOverrides builds clientcmd overrides that rewrite the file paths the
// host kubeconfig references (CA certificate, client certificate, client key)
// so they resolve against the host filesystem mount. Credentials embedded as
// *-data are left untouched. The CA falls back to the well-known host location
// when the kubeconfig references neither a CA file nor embedded data.
func (rcp *podRestConfigProvider) hostPathOverrides(kubeconfigPath string) (*clientcmd.ConfigOverrides, error) {
	hostRoot := config.HostRoot()

	kubeconfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig %q: %w", kubeconfigPath, err)
	}

	overrides := &clientcmd.ConfigOverrides{}

	if cluster := clusterForCurrentContext(kubeconfig); cluster != nil && len(cluster.CertificateAuthorityData) == 0 {
		if caCertPath := toHostPath(hostRoot, cluster.CertificateAuthority); caCertPath != "" {
			overrides.ClusterInfo.CertificateAuthority = caCertPath
		} else if fallback := pathlib.ResolveCACertPath(hostRoot); fallback != "" {
			overrides.ClusterInfo.CertificateAuthority = fallback
		} else {
			return nil, fmt.Errorf("could not locate host CA Certificates in expected paths")
		}
	}

	if authInfo := authInfoForCurrentContext(kubeconfig); authInfo != nil {
		if len(authInfo.ClientCertificateData) == 0 {
			overrides.AuthInfo.ClientCertificate = toHostPath(hostRoot, authInfo.ClientCertificate)
		}
		if len(authInfo.ClientKeyData) == 0 {
			overrides.AuthInfo.ClientKey = toHostPath(hostRoot, authInfo.ClientKey)
		}
	}

	return overrides, nil
}

// toHostPath prefixes a host-absolute path with the host root mount, unless the
// path is empty or already points inside the mount.
func toHostPath(hostRoot, path string) string {
	if path == "" || hostRoot == "/" || strings.HasPrefix(path, hostRoot) {
		return path
	}
	return filepath.Join(hostRoot, path)
}

// clusterForCurrentContext returns the cluster referenced by the kubeconfig's
// current context, falling back to the sole cluster when unambiguous.
func clusterForCurrentContext(kubeconfig *clientcmdapi.Config) *clientcmdapi.Cluster {
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

// authInfoForCurrentContext returns the user referenced by the kubeconfig's
// current context, falling back to the sole user when unambiguous.
func authInfoForCurrentContext(kubeconfig *clientcmdapi.Config) *clientcmdapi.AuthInfo {
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

type chrootMapper struct{}

func (c *chrootMapper) Map(command string, args ...string) (string, []string, error) {
	// shift the original command and arguments and call a go wrapper for chroot
	// because its not available on the system.
	newArgs := append([]string{config.HostRoot(), command}, args...)
	executable, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get executable: %w", err)
	}
	// use the chroot wrapper we've built.
	chroot := filepath.Join(filepath.Dir(executable), "chroot")
	return chroot, newArgs, nil
}
