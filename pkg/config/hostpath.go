package config

import (
	"os"
	"path/filepath"
)

const HOST_ROOT_ENV = "HOST_ROOT"

const VPC_CNI_POD_PREFIX_ENV = "VPC_CNI_POD_PREFIX"

// HostRoot returns the root path for accessing host filesystem
// Defaults to "/" if HOST_ROOT environment variable is not set
func HostRoot() string {
	if root, exists := os.LookupEnv(HOST_ROOT_ENV); exists {
		return root
	}
	return "/"
}

// VPCCNIPodPrefix returns the substring used to match VPC CNI pod log directories
// in /var/log/pods/. Defaults to the upstream "aws-node" DaemonSet name; override
// via the VPC_CNI_POD_PREFIX environment variable for installs with a custom name.
func VPCCNIPodPrefix() string {
	if prefix, exists := os.LookupEnv(VPC_CNI_POD_PREFIX_ENV); exists {
		return prefix
	}
	return "_aws-node-"
}

// ToHostPath joins the host root with the given path
func ToHostPath(path string) string {
	return filepath.Join(HostRoot(), path)
}

// Common paths
var (
	SystemMessagesPath = ToHostPath("/var/log/messages")
	PodLogsDirPath     = ToHostPath("/var/log/pods/")
	PCIDevicesPath     = ToHostPath("/proc/bus/pci/devices")
	CRIEndpoint        = "unix://" + ToHostPath("/run/containerd/containerd.sock")
	IPAMDLogPath       = ToHostPath("/var/log/aws-routed-eni/ipamd.log")
	NPALogPath         = ToHostPath("/var/log/aws-routed-eni/network-policy-agent.log")
)
