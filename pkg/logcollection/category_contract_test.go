package logcollection_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/logcollection"
)

// pod log directories under the fake host root, named like the kubelet names
// them: /var/log/pods/<namespace>_<pod>_<uid>/<container>
const (
	awsNodePod     = "kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-node"
	ebsCSIPod      = "kube-system_ebs-csi-node-x4fkz_11111111-2222-3333-4444-555555555555/ebs-plugin"
	podIdentityPod = "kube-system_eks-pod-identity-agent-7rmwl_66666666-7777-8888-9999-000000000000/eks-pod-identity-agent"
)

// fakeRoot builds a host root with /var/log files owned by Base and one pod
// log directory owned by each category that collects pod logs.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/log"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"messages", "user-data.log"} {
		if err := os.WriteFile(filepath.Join(root, "var/log", f), []byte(f+" contents\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, pod := range []string{awsNodePod, ebsCSIPod, podIdentityPod} {
		dir := filepath.Join(root, "var/log/pods", pod)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "0.log"), []byte("log line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runCommonLogs(t *testing.T, root, dst string, category v1alpha1.LogCategory) {
	t.Helper()
	acc, err := collect.NewAccessor(context.Background(), collect.Config{Root: root, Destination: dst})
	if err != nil {
		t.Fatalf("NewAccessor: %v", err)
	}
	for _, c := range logcollection.GetCollectors(category) {
		if cl, ok := c.(*collect.CommonLogs); ok {
			if err := cl.Collect(acc); err != nil {
				t.Fatalf("Collect: %v", err)
			}
		}
	}
}

// TestCategoryCommonLogsContract pins which /var/log files and pod logs each
// category's CommonLogs collectors capture: Base owns the /var/log files and
// no pod logs; Device, Networking and System own only their pod allowlists;
// Runtime has no CommonLogs collector at all. An explicitly empty list means
// "collect none of this kind" — it must not expand to the defaults.
func TestCategoryCommonLogsContract(t *testing.T) {
	tests := []struct {
		category v1alpha1.LogCategory
		present  []string
		absent   []string
	}{
		{
			category: v1alpha1.LogCategoryBase,
			present:  []string{"var_log/messages", "var_log/user-data.log"},
			absent:   []string{"var_log/" + awsNodePod, "var_log/" + ebsCSIPod, "var_log/" + podIdentityPod},
		},
		{
			category: v1alpha1.LogCategoryDevice,
			present:  []string{"var_log/" + ebsCSIPod + "/0.log"},
			absent:   []string{"var_log/messages", "var_log/user-data.log", "var_log/" + awsNodePod},
		},
		{
			category: v1alpha1.LogCategoryNetworking,
			present:  []string{"var_log/" + awsNodePod + "/0.log"},
			absent:   []string{"var_log/messages", "var_log/user-data.log", "var_log/" + ebsCSIPod},
		},
		{
			category: v1alpha1.LogCategoryRuntime,
			absent: []string{
				"var_log/messages", "var_log/user-data.log",
				"var_log/" + awsNodePod, "var_log/" + ebsCSIPod, "var_log/" + podIdentityPod,
			},
		},
		{
			category: v1alpha1.LogCategorySystem,
			present:  []string{"var_log/" + podIdentityPod + "/0.log"},
			absent:   []string{"var_log/messages", "var_log/user-data.log", "var_log/" + awsNodePod},
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.category), func(t *testing.T) {
			root := fakeRoot(t)
			dst := t.TempDir()
			runCommonLogs(t, root, dst, tc.category)

			for _, rel := range tc.present {
				if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
					t.Errorf("category %s: expected %s to be collected: %v", tc.category, rel, err)
				}
			}
			for _, rel := range tc.absent {
				if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
					t.Errorf("category %s: expected %s to be absent, got err=%v", tc.category, rel, err)
				}
			}
		})
	}
}

// TestAllCategoryCollectsVarLogFilesOnce verifies that under the All category
// exactly one CommonLogs instance (Base's) collects the /var/log files, so the
// same file is not read and copied redundantly by multiple instances.
func TestAllCategoryCollectsVarLogFilesOnce(t *testing.T) {
	root := fakeRoot(t)

	count := 0
	for _, c := range logcollection.GetCollectors(v1alpha1.LogCategoryAll) {
		cl, ok := c.(*collect.CommonLogs)
		if !ok {
			continue
		}
		dst := t.TempDir()
		acc, err := collect.NewAccessor(context.Background(), collect.Config{Root: root, Destination: dst})
		if err != nil {
			t.Fatalf("NewAccessor: %v", err)
		}
		if err := cl.Collect(acc); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "var_log/messages")); err == nil {
			count++
			// the instance that owns /var/log files must also collect user-data.log
			if _, err := os.Stat(filepath.Join(dst, "var_log/user-data.log")); err != nil {
				t.Errorf("instance collecting messages did not collect user-data.log: %v", err)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 CommonLogs instance to collect /var/log/messages under All, got %d", count)
	}
}
