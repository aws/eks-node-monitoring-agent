package collect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type CommonLogs struct {
	PodLogPatterns []string
	VarLogFiles    []string
}

var _ Collector = (*CommonLogs)(nil)

var DefaultVarLogFiles = []string{
	"syslog",
	"messages",
	"aws-routed-eni",
	"cron",
	"cloud-init.log",
	"cloud-init-output.log",
	"user-data.log",
	"kube-proxy.log",
}

var DefaultPodLogPatterns = []string{
	"kube-system_aws-node*",
	"kube-system_cni-metrics-helper*",
	"kube-system_coredns*",
	"kube-system_kube-proxy*",
	"kube-system_ebs-csi-*",
	"kube-system_efs-csi-*",
	"kube-system_fsx-csi-*",
	"kube-system_fsx-openzfs-csi-*",
	"kube-system_file-cache-csi-*",
	"kube-system_eks-pod-identity-agent*",
}

func (c *CommonLogs) Collect(acc *Accessor) error {
	if c.PodLogPatterns != nil && len(c.PodLogPatterns) == 0 {
		c.PodLogPatterns = DefaultPodLogPatterns
	}
	if c.VarLogFiles != nil && len(c.VarLogFiles) == 0 {
		c.VarLogFiles = DefaultVarLogFiles
	}
	return errors.Join(
		common(acc, c.VarLogFiles),
		pods(acc, c.PodLogPatterns),
	)
}

func common(acc *Accessor, logFiles []string) error {
	var merr error
	for _, logName := range logFiles {
		savePath := filepath.Join("var_log/", logName)
		fullPath := filepath.Join(acc.cfg.Root, "/var/log/", logName)
		if st, err := os.Stat(fullPath); err != nil {
			continue
		} else if st.IsDir() {
			merr = errors.Join(merr, acc.CopyDir(fullPath, savePath))
		} else {
			merr = errors.Join(merr, acc.CopyFile(fullPath, savePath))
		}
	}
	return merr
}

// NOTE: /var/log/containers paths are symlinked to /var/log/pods/../*
func pods(acc *Accessor, podLogPatterns []string) error {
	var merr error
	// pod paths formatted like:
	// ---
	// /var/log/pods/kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe
	// /var/log/pods/kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-vpc-cni-init
	// /var/log/pods/kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-vpc-cni-init/0.log
	// /var/log/pods/kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-node
	// /var/log/pods/kube-system_aws-node-z8qk6_892ff6b4-95ee-409c-87ea-c97686b644fe/aws-node/0.log
	for _, pod := range podLogPatterns {
		podsDir := filepath.Join(acc.cfg.Root, "/var/log/pods/")
		podNames, err := filepath.Glob(filepath.Join(podsDir, pod))
		if err != nil {
			merr = errors.Join(merr, err)
			continue
		}
		for _, logName := range podNames {
			merr = errors.Join(merr, acc.CopyDir(logName, filepath.Join("var_log/", strings.TrimPrefix(logName, podsDir))))
		}
	}
	return merr
}
