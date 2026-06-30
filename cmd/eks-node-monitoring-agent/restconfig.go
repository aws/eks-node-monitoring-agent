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

	// the CA the cluster is configured with is the source of truth. it may be
	// embedded in the kubeconfig (certificate-authority-data) or referenced as a
	// host file path (certificate-authority); fall back to the well-known host
	// location only when the kubeconfig specifies neither.
	overrides := &clientcmd.ConfigOverrides{}
	if caCertPath, err := rcp.resolveCACertPath(kubeconfigPath); err != nil {
		return nil, err
	} else if caCertPath != "" {
		overrides.ClusterInfo = clientcmdapi.Cluster{CertificateAuthority: caCertPath}
	}

	// attempt to pick up kubelet's cluster config from the node.
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}

	if err := rewriteExecProvider(restConfig, rcp.execMapper); err != nil {
		return nil, err
	}

	return restConfig, nil
}

// rewriteExecProvider transforms the exec provider arguments, because they need
// to call host binaries in order to get a token from the iam authenticator.
// NOTE: reminder that these calls also utilize hostNetworking in order to
// reach IMDS to get host credentials.
// kubeconfigs that authenticate without an exec credential plugin (e.g. a
// client certificate) have no ExecProvider, so only rewrite it when present.
func rewriteExecProvider(restConfig *rest.Config, m chrootMapper) error {
	if restConfig.ExecProvider == nil {
		return nil
	}
	var err error
	restConfig.ExecProvider.Command, restConfig.ExecProvider.Args, err = m.Map(
		restConfig.ExecProvider.Command,
		restConfig.ExecProvider.Args...,
	)
	return err
}

// resolveCACertPath determines the CA certificate the host kubeconfig expects.
// It returns an empty path (no override needed) when the kubeconfig already
// embeds certificate-authority-data, the host-root-prefixed path when the
// kubeconfig references a CA file, and the well-known default location as a
// last resort.
func (rcp *podRestConfigProvider) resolveCACertPath(kubeconfigPath string) (string, error) {
	hostRoot := config.HostRoot()

	kubeconfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to load kubeconfig %q: %w", kubeconfigPath, err)
	}

	cluster := clusterForCurrentContext(kubeconfig)
	if cluster != nil {
		// embedded CA data is self-contained; let clientcmd use it as-is.
		if len(cluster.CertificateAuthorityData) > 0 {
			return "", nil
		}
		// a referenced CA file path is relative to the host, so prefix it with
		// the host root unless it already points inside the mount.
		if caCertPath := cluster.CertificateAuthority; caCertPath != "" {
			if hostRoot != "/" && !strings.HasPrefix(caCertPath, hostRoot) {
				caCertPath = filepath.Join(hostRoot, caCertPath)
			}
			return caCertPath, nil
		}
	}

	if caCertPath := pathlib.ResolveCACertPath(hostRoot); caCertPath != "" {
		return caCertPath, nil
	}
	return "", fmt.Errorf("could not locate host CA Certificates in expected paths")
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
