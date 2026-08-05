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

func TestResolveCACertPath(t *testing.T) {
	t.Run("resolves the CA of the kubeconfig's current context", func(t *testing.T) {
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

	t.Run("errors when the kubeconfig cannot be loaded", func(t *testing.T) {
		if _, err := (&podRestConfigProvider{}).resolveCACertPath("/does/not/exist"); err == nil {
			t.Fatal("expected an error for a missing kubeconfig")
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
