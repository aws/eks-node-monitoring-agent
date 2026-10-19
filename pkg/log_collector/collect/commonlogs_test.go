package collect_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
)

func TestCommonLogsPodLogPatterns(t *testing.T) {
	// pod log directories under the fake host root, named like the kubelet
	// does: /var/log/pods/<namespace>_<pod>_<uid>/<container>
	collected := []string{
		"kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-node",
		"kube-system_s3-csi-node-x4fkz_11111111-2222-3333-4444-555555555555/s3-plugin",
		"mount-s3_mp-a1b2c_66666666-7777-8888-9999-000000000000/mountpoint",
		"mount-s3_hr-d4e5f_aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/headroom",
		"kube-system_eks-node-monitoring-agent-7rmwl_ffffffff-0000-1111-2222-333333333333/node-monitoring-agent",
		"kube-system_kube-multus-ds-8xk2p_44444444-5555-6666-7777-888888888888/kube-multus",
	}
	notCollected := []string{
		"default_my-app-5d9c7_99999999-8888-7777-6666-555555555555/app",
		"kube-system_metrics-server-abc12_12121212-3434-5656-7878-909090909090/metrics-server",
	}

	root, dst := t.TempDir(), t.TempDir()
	for _, pod := range append(append([]string{}, collected...), notCollected...) {
		dir := filepath.Join(root, "var/log/pods", pod)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("setup: mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "0.log"), []byte("log line\n"), 0o644); err != nil {
			t.Fatalf("setup: write %s: %v", dir, err)
		}
	}

	acc, err := collect.NewAccessor(context.Background(), collect.Config{
		Root:        root,
		Destination: dst,
	})
	if err != nil {
		t.Fatalf("NewAccessor: %v", err)
	}

	c := &collect.CommonLogs{
		PodLogPatterns: collect.DefaultPodLogPatterns,
		VarLogFiles:    collect.DefaultVarLogFiles,
	}
	if err := c.Collect(acc); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, pod := range collected {
		if _, err := os.Stat(filepath.Join(dst, "var_log", pod, "0.log")); err != nil {
			t.Errorf("expected pod logs %s to be collected: %v", pod, err)
		}
	}
	for _, pod := range notCollected {
		if _, err := os.Stat(filepath.Join(dst, "var_log", pod, "0.log")); !os.IsNotExist(err) {
			t.Errorf("expected pod logs %s to be absent, got err=%v", pod, err)
		}
	}
}
