package main

import (
	"os"
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

func TestResolveCACertPath(t *testing.T) {
	t.Run("embedded CA data needs no override", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{
			CertificateAuthorityData: []byte("---embedded ca---"),
		})

		caCertPath, err := (&podRestConfigProvider{}).resolveCACertPath(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if caCertPath != "" {
			t.Fatalf("expected empty path for embedded CA data, got %q", caCertPath)
		}
	})

	t.Run("referenced CA file is prefixed with the host root", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{
			CertificateAuthority: "/etc/kubernetes/pki/ca.crt",
		})

		caCertPath, err := (&podRestConfigProvider{}).resolveCACertPath(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(hostRoot, "/etc/kubernetes/pki/ca.crt"); caCertPath != want {
			t.Fatalf("expected %q, got %q", want, caCertPath)
		}
	})

	t.Run("referenced CA file already inside the host root is left as-is", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		caCertPath := filepath.Join(hostRoot, "/etc/kubernetes/pki/ca.crt")
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{
			CertificateAuthority: caCertPath,
		})

		got, err := (&podRestConfigProvider{}).resolveCACertPath(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != caCertPath {
			t.Fatalf("expected %q unchanged, got %q", caCertPath, got)
		}
	})

	t.Run("falls back to the well-known host location", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		wellKnown := filepath.Join(hostRoot, "/etc/kubernetes/pki/ca.crt")
		if err := os.MkdirAll(filepath.Dir(wellKnown), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wellKnown, []byte("---ca---"), 0o644); err != nil {
			t.Fatal(err)
		}
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{})

		got, err := (&podRestConfigProvider{}).resolveCACertPath(kubeconfigPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != wellKnown {
			t.Fatalf("expected %q, got %q", wellKnown, got)
		}
	})

	t.Run("errors when no CA can be located", func(t *testing.T) {
		hostRoot := t.TempDir()
		t.Setenv(config.HOST_ROOT_ENV, hostRoot)
		kubeconfigPath := writeKubeconfig(t, hostRoot, &clientcmdapi.Cluster{})

		if _, err := (&podRestConfigProvider{}).resolveCACertPath(kubeconfigPath); err == nil {
			t.Fatal("expected an error when no CA can be located")
		}
	})

	t.Run("errors when the kubeconfig cannot be loaded", func(t *testing.T) {
		if _, err := (&podRestConfigProvider{}).resolveCACertPath("/does/not/exist"); err == nil {
			t.Fatal("expected an error for a missing kubeconfig")
		}
	})
}

func TestClusterForCurrentContext(t *testing.T) {
	t.Run("resolves the cluster referenced by the current context", func(t *testing.T) {
		want := &clientcmdapi.Cluster{Server: "https://current"}
		kubeconfig := &clientcmdapi.Config{
			CurrentContext: "ctx",
			Contexts:       map[string]*clientcmdapi.Context{"ctx": {Cluster: "wanted"}},
			Clusters: map[string]*clientcmdapi.Cluster{
				"wanted": want,
				"other":  {Server: "https://other"},
			},
		}
		if got := clusterForCurrentContext(kubeconfig); got != want {
			t.Fatalf("expected %+v, got %+v", want, got)
		}
	})

	t.Run("falls back to the sole cluster when the context is missing", func(t *testing.T) {
		want := &clientcmdapi.Cluster{Server: "https://only"}
		kubeconfig := &clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{"only": want},
		}
		if got := clusterForCurrentContext(kubeconfig); got != want {
			t.Fatalf("expected the sole cluster, got %+v", got)
		}
	})

	t.Run("returns nil when the context is missing and clusters are ambiguous", func(t *testing.T) {
		kubeconfig := &clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				"a": {Server: "https://a"},
				"b": {Server: "https://b"},
			},
		}
		if got := clusterForCurrentContext(kubeconfig); got != nil {
			t.Fatalf("expected nil for ambiguous clusters, got %+v", got)
		}
	})
}

// writeKubeconfig writes a minimal kubeconfig with a single "test" cluster/context
// under hostRoot and returns its path.
func writeKubeconfig(t *testing.T, hostRoot string, cluster *clientcmdapi.Cluster) string {
	t.Helper()
	kubeconfig := clientcmdapi.Config{
		CurrentContext: "test",
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test"}},
		Clusters:       map[string]*clientcmdapi.Cluster{"test": cluster},
	}
	kubeconfigPath := filepath.Join(hostRoot, "kubeconfig")
	if err := clientcmd.WriteToFile(kubeconfig, kubeconfigPath); err != nil {
		t.Fatal(err)
	}
	return kubeconfigPath
}
