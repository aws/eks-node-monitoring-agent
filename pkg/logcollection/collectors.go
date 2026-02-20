package logcollection

import (
	"slices"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
)

// GetCollectors returns a list of callable collectors based on the categories
// passed, and will return all valid collectors if `All` is a provided category.
func GetCollectors(categories ...v1alpha1.LogCategory) []collect.Collector {
	selectedCollectors := []collect.Collector{}

	if slices.Contains(categories, v1alpha1.LogCategoryAll) {
		for _, collectors := range collectorMap {
			selectedCollectors = append(selectedCollectors, collectors...)
		}
		return selectedCollectors
	}

	for category, collectors := range collectorMap {
		if slices.Contains(categories, category) {
			selectedCollectors = append(selectedCollectors, collectors...)
		}
	}

	return selectedCollectors
}

var collectorMap = map[v1alpha1.LogCategory][]collect.Collector{
	v1alpha1.LogCategoryBase: {
		&collect.CommonLogs{
			PodLogPatterns: []string{},
			VarLogFiles: []string{
				"syslog",
				"messages",
				"aws-routed-eni",
				"cron",
				"cloud-init.log",
				"cloud-init-output.log",
				"kube-proxy.log",
			},
		},
		&collect.Instance{},
		&collect.Region{},
	},
	v1alpha1.LogCategoryDevice: {
		&collect.CommonLogs{
			VarLogFiles: []string{},
			PodLogPatterns: []string{
				"kube-system_ebs-csi-*",
				"kube-system_efs-csi-*",
				"kube-system_fsx-csi-*",
				"kube-system_fsx-openzfs-csi-*",
				"kube-system_file-cache-csi-*",
			},
		},
		&collect.Nvidia{},
	},
	v1alpha1.LogCategoryNetworking: {
		&collect.CNI{},
		&collect.CommonLogs{
			VarLogFiles: []string{},
			PodLogPatterns: []string{
				"kube-system_aws-node*",
				"kube-system_cni-metrics-helper*",
				"kube-system_coredns*",
				"kube-system_kube-proxy*",
			},
		},
		&collect.IPAMD{},
		&collect.IPTables{},
		&collect.Networking{},
		&collect.NFTables{},
	},
	v1alpha1.LogCategoryRuntime: {
		&collect.Containerd{},
		&collect.Kubernetes{},
		&collect.Nodeadm{},
		&collect.Sandbox{},
	},
	v1alpha1.LogCategorySystem: {
		&collect.CommonLogs{
			VarLogFiles: []string{},
			PodLogPatterns: []string{
				"kube-system_eks-pod-identity-agent*",
			},
		},
		&collect.Disk{},
		&collect.Kernel{},
		&collect.SELinux{},
		&collect.System{},
		&collect.Throttles{},
	},
}
