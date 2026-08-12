package main

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func TestRewriteExecProvider(t *testing.T) {
	t.Run("nil exec provider is left untouched", func(t *testing.T) {
		cfg := &rest.Config{} // client-cert auth, no ExecProvider
		if err := rewriteExecProvider(cfg, chrootMapper{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ExecProvider != nil {
			t.Fatal("ExecProvider should stay nil")
		}
	})

	t.Run("exec provider is rewritten through the chroot wrapper", func(t *testing.T) {
		cfg := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Command: "aws-iam-authenticator",
				Args:    []string{"token", "-i", "cluster"},
			},
		}
		if err := rewriteExecProvider(cfg, chrootMapper{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(cfg.ExecProvider.Command) != "chroot" {
			t.Fatalf("command not rewritten to chroot wrapper: %q", cfg.ExecProvider.Command)
		}
		if cfg.ExecProvider.Args[1] != "aws-iam-authenticator" {
			t.Fatalf("original command not preserved in args: %v", cfg.ExecProvider.Args)
		}
	})
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
