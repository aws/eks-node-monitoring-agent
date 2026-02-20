package main

import (
	"fmt"
	"os"
	"path/filepath"

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
	caCertPath := pathlib.ResolveCACertPath(config.HostRoot())
	if caCertPath == "" {
		return nil, fmt.Errorf("could not locate host CA Certificates in expected paths")
	}

	// attempt to pick up kubelet's cluster config from the node.
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{CertificateAuthority: caCertPath}},
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
