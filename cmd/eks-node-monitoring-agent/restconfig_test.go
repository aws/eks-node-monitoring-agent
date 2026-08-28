package main

import (
	"net/http"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/aws/eks-node-monitoring-agent/pkg/auth"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func TestUseInProcessTokenAuth(t *testing.T) {
	t.Run("nil exec provider is left untouched", func(t *testing.T) {
		cfg := &rest.Config{} // client-cert auth, no ExecProvider
		if err := useInProcessTokenAuth(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ExecProvider != nil {
			t.Fatal("ExecProvider should stay nil")
		}
		if cfg.WrapTransport != nil {
			t.Fatal("no token transport should be installed without an ExecProvider")
		}
	})

	t.Run("exec provider is replaced by the token transport", func(t *testing.T) {
		cfg := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Command: "aws-iam-authenticator",
				Args:    []string{"token", "-i", "cluster"},
			},
		}
		if err := useInProcessTokenAuth(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// the ExecProvider must be cleared, otherwise client-go still launches
		// the plugin and the memory spike this change avoids comes back.
		if cfg.ExecProvider != nil {
			t.Fatal("ExecProvider should be cleared")
		}
		if cfg.WrapTransport == nil {
			t.Fatal("expected a token transport to be installed")
		}
		wrapped := cfg.WrapTransport(http.DefaultTransport)
		tokenTransport, ok := wrapped.(*auth.EKSTokenTransport)
		if !ok {
			t.Fatalf("expected an *auth.EKSTokenTransport, got %T", wrapped)
		}
		if tokenTransport.Base != http.DefaultTransport {
			t.Fatal("expected the wrapped transport to be used as the base")
		}
	})

	t.Run("an existing transport wrapper is preserved", func(t *testing.T) {
		wrapped := false
		cfg := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Command: "aws",
				Args:    []string{"eks", "get-token", "--cluster-name", "cluster"},
			},
		}
		cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
			wrapped = true
			return rt
		})
		if err := useInProcessTokenAuth(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cfg.WrapTransport(http.DefaultTransport)
		if !wrapped {
			t.Fatal("the pre-existing transport wrapper was dropped")
		}
	})

	t.Run("errors when the cluster name is missing", func(t *testing.T) {
		cfg := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Command: "aws",
				Args:    []string{"eks", "get-token"},
			},
		}
		if err := useInProcessTokenAuth(cfg); err == nil {
			t.Fatal("expected an error when the cluster name cannot be determined")
		}
	})
}

func TestClusterNameFromExecProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"aws eks get-token", []string{"eks", "get-token", "--cluster-name", "my-cluster"}, "my-cluster"},
		{"aws eks get-token with region", []string{"--region", "us-west-2", "eks", "get-token", "--cluster-name", "my-cluster", "--output", "json"}, "my-cluster"},
		{"joined with equals", []string{"eks", "get-token", "--cluster-name=my-cluster"}, "my-cluster"},
		{"aws-iam-authenticator short flag", []string{"token", "-i", "my-cluster"}, "my-cluster"},
		{"aws-iam-authenticator long flag", []string{"token", "--cluster-id", "my-cluster"}, "my-cluster"},
		{"short flag joined with equals", []string{"token", "-i=my-cluster"}, "my-cluster"},
		{"surrounding whitespace is trimmed", []string{"token", "-i", "  my-cluster  "}, "my-cluster"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clusterNameFromExecProvider(&clientcmdapi.ExecConfig{Args: tc.args})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"no cluster flag", []string{"eks", "get-token", "--region", "us-west-2"}},
		{"flag with no value", []string{"eks", "get-token", "--cluster-name"}},
		{"blank value", []string{"token", "-i", "   "}},
		{"blank joined value", []string{"token", "--cluster-id="}},
	} {
		t.Run("errors on "+tc.name, func(t *testing.T) {
			if _, err := clusterNameFromExecProvider(&clientcmdapi.ExecConfig{Args: tc.args}); err == nil {
				t.Fatalf("expected an error for args %q", tc.args)
			}
		})
	}
}

func TestHostPathOverrides(t *testing.T) {
	t.Run("resolves the CA of the kubeconfig's current context", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{
			CertificateAuthority: "/etc/kubernetes/pki/ca.crt",
		}, nil)

		overrides, err := (&podRestConfigProvider{}).hostPathOverrides(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(hostRoot, "/etc/kubernetes/pki/ca.crt"); overrides.ClusterInfo.CertificateAuthority != want {
			t.Fatalf("expected %q, got %q", want, overrides.ClusterInfo.CertificateAuthority)
		}
	})

	t.Run("resolves client certificate and key paths against the host root", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot,
			&clientcmdapi.Cluster{CertificateAuthority: "/srv/kubernetes/api-server-ca-bundle.crt"},
			&clientcmdapi.AuthInfo{
				ClientCertificate: "/srv/kubernetes/kubelet.crt",
				ClientKey:         "/srv/kubernetes/kubelet.key",
			},
		)

		overrides, err := (&podRestConfigProvider{}).hostPathOverrides(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(hostRoot, "/srv/kubernetes/kubelet.crt"); overrides.AuthInfo.ClientCertificate != want {
			t.Fatalf("expected client certificate %q, got %q", want, overrides.AuthInfo.ClientCertificate)
		}
		if want := filepath.Join(hostRoot, "/srv/kubernetes/kubelet.key"); overrides.AuthInfo.ClientKey != want {
			t.Fatalf("expected client key %q, got %q", want, overrides.AuthInfo.ClientKey)
		}
	})

	t.Run("resolves the token file against the host root", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot,
			&clientcmdapi.Cluster{CertificateAuthorityData: []byte("ca")},
			&clientcmdapi.AuthInfo{TokenFile: "/var/lib/kubelet/token"},
		)

		overrides, err := (&podRestConfigProvider{}).hostPathOverrides(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(hostRoot, "/var/lib/kubelet/token"); overrides.AuthInfo.TokenFile != want {
			t.Fatalf("expected token file %q, got %q", want, overrides.AuthInfo.TokenFile)
		}
	})

	t.Run("leaves the token file untouched when an inline token takes precedence", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot,
			&clientcmdapi.Cluster{CertificateAuthorityData: []byte("ca")},
			&clientcmdapi.AuthInfo{Token: "inline", TokenFile: "/var/lib/kubelet/token"},
		)

		overrides, err := (&podRestConfigProvider{}).hostPathOverrides(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if overrides.AuthInfo.TokenFile != "" {
			t.Fatalf("expected no token file override, got %q", overrides.AuthInfo.TokenFile)
		}
	})

	t.Run("leaves embedded credentials untouched", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot,
			&clientcmdapi.Cluster{CertificateAuthorityData: []byte("ca")},
			&clientcmdapi.AuthInfo{
				ClientCertificateData: []byte("cert"),
				ClientKeyData:         []byte("key"),
			},
		)

		overrides, err := (&podRestConfigProvider{}).hostPathOverrides(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if overrides.ClusterInfo.CertificateAuthority != "" {
			t.Fatalf("expected no CA override, got %q", overrides.ClusterInfo.CertificateAuthority)
		}
		if overrides.AuthInfo.ClientCertificate != "" || overrides.AuthInfo.ClientKey != "" {
			t.Fatalf("expected no client credential overrides, got %q / %q",
				overrides.AuthInfo.ClientCertificate, overrides.AuthInfo.ClientKey)
		}
	})

	t.Run("errors when the kubeconfig cannot be loaded", func(t *testing.T) {
		if _, err := (&podRestConfigProvider{}).hostPathOverrides("/does/not/exist"); err == nil {
			t.Fatal("expected an error for a missing kubeconfig")
		}
	})
}

// writeKubeconfig writes a minimal kubeconfig with a single "test"
// cluster/user/context under hostRoot and returns its path.
func writeKubeconfig(t *testing.T, hostRoot string, cluster *clientcmdapi.Cluster, authInfo *clientcmdapi.AuthInfo) string {
	t.Helper()
	kubeconfig := clientcmdapi.Config{
		CurrentContext: "test",
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
		Clusters:       map[string]*clientcmdapi.Cluster{"test": cluster},
	}
	if authInfo != nil {
		kubeconfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{"test": authInfo}
	}
	kubeconfigPath := filepath.Join(hostRoot, "kubeconfig")
	if err := clientcmd.WriteToFile(kubeconfig, kubeconfigPath); err != nil {
		t.Fatal(err)
	}
	return kubeconfigPath
}
