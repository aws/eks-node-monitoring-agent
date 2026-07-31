package main

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
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
