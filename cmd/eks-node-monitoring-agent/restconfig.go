package main

import (
	"fmt"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/aws/eks-node-monitoring-agent/pkg/auth"
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
	return &podRestConfigProvider{}
}

type podRestConfigProvider struct{}

func (rcp *podRestConfigProvider) Provide() (*rest.Config, error) {
	kubeconfigPath := pathlib.ResolveKubeconfig(config.HostRoot())
	if kubeconfigPath == "" {
		return nil, fmt.Errorf("could not locate host kubeconfig in expected paths")
	}

	// the kubeconfig references its TLS material by path relative to the host,
	// but the agent reads the host filesystem through a mount, so build
	// overrides that resolve those paths against it.
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

	if err := useInProcessTokenAuth(restConfig); err != nil {
		return nil, err
	}

	return restConfig, nil
}

// useInProcessTokenAuth replaces the kubeconfig's exec credential plugin with
// in-process token generation.
//
// The plugin (`aws eks get-token` or `aws-iam-authenticator token`) only exists
// on the host, so the agent used to invoke it by chrooting onto the host root.
// That cost a process launch — and the aws CLI's ~50MB resident set — every time
// client-go refreshed the credential, roughly every 15 minutes, which was enough
// to push the container past its cgroup memory limit and get it OOM killed.
// Generating the token in-process with the same library removes the launch
// entirely; see pkg/auth for how the token is minted and cached.
//
// NOTE: the token is a presigned STS request, so this still depends on the pod
// reaching IMDS to pick up the node's instance profile — which is why the agent
// runs with hostNetwork, as it did for the chrooted plugin.
func useInProcessTokenAuth(restConfig *rest.Config) error {
	// kubeconfigs that authenticate with a client certificate or a token file
	// (e.g. on kops-provisioned nodes) carry no credential plugin, and client-go
	// reads that material directly. Nothing to replace.
	if restConfig.ExecProvider == nil {
		return nil
	}

	clusterName, err := clusterNameFromExecProvider(restConfig.ExecProvider)
	if err != nil {
		return err
	}

	generator, err := auth.NewEKSTokenGenerator(clusterName)
	if err != nil {
		return fmt.Errorf("failed to create EKS token generator: %w", err)
	}

	// clearing the ExecProvider is what stops client-go from launching the
	// plugin; the transport supplies the Authorization header in its place.
	restConfig.ExecProvider = nil
	restConfig.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &auth.EKSTokenTransport{Base: rt, Generator: generator}
	})

	return nil
}

// clusterNameFromExecProvider reads the cluster name out of the credential
// plugin's arguments. The kubeconfig is the authoritative source here — it is
// where the plugin itself read the name from — but the flag it uses depends on
// which plugin the kubeconfig was written for: `aws eks get-token` takes
// --cluster-name, while `aws-iam-authenticator token` takes --cluster-id (-i).
// Either may spell its value as a separate argument or joined with '='.
func clusterNameFromExecProvider(execConfig *clientcmdapi.ExecConfig) (string, error) {
	for i, arg := range execConfig.Args {
		flag, value, joined := strings.Cut(arg, "=")
		switch flag {
		case "--cluster-name", "--cluster-id", "-i":
		default:
			continue
		}
		if !joined {
			if i+1 >= len(execConfig.Args) {
				continue
			}
			value = execConfig.Args[i+1]
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("could not determine the cluster name from exec credential plugin args %q", execConfig.Args)
}

// hostPathOverrides builds clientcmd overrides that resolve the file paths the
// host kubeconfig references (CA certificate, client certificate, client key,
// token file) against the host filesystem mount. Credentials embedded in the
// kubeconfig are self-contained and left untouched.
func (rcp *podRestConfigProvider) hostPathOverrides(kubeconfigPath string) (*clientcmd.ConfigOverrides, error) {
	hostRoot := config.HostRoot()

	kubeconfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig %q: %w", kubeconfigPath, err)
	}

	overrides := &clientcmd.ConfigOverrides{}

	// the CA the cluster is configured with is the source of truth. it may be
	// embedded in the kubeconfig (certificate-authority-data) or referenced as a
	// host file path (certificate-authority); fall back to the well-known host
	// location only when the kubeconfig specifies neither.
	caCertPath, err := pathlib.ResolveClusterCACert(hostRoot, pathlib.ClusterForCurrentContext(kubeconfig))
	if err != nil {
		return nil, err
	}
	if caCertPath != "" {
		overrides.ClusterInfo = clientcmdapi.Cluster{CertificateAuthority: caCertPath}
	}

	// kubeconfigs that authenticate with a client certificate (e.g. on
	// kops-provisioned nodes) reference the certificate and key by host path too,
	// as does a token read from a file (tokenFile).
	if authInfo := pathlib.AuthInfoForCurrentContext(kubeconfig); authInfo != nil {
		if len(authInfo.ClientCertificateData) == 0 {
			overrides.AuthInfo.ClientCertificate = pathlib.HostPath(hostRoot, authInfo.ClientCertificate)
		}
		if len(authInfo.ClientKeyData) == 0 {
			overrides.AuthInfo.ClientKey = pathlib.HostPath(hostRoot, authInfo.ClientKey)
		}
		// an inline token takes precedence over tokenFile, so only the latter
		// needs a path.
		if len(authInfo.Token) == 0 {
			overrides.AuthInfo.TokenFile = pathlib.HostPath(hostRoot, authInfo.TokenFile)
		}
	}

	return overrides, nil
}
