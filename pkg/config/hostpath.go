package config

import (
	"os"
	"path/filepath"
)

const HOST_ROOT_ENV = "HOST_ROOT"
const NVIDIA_DRIVER_ROOT_ENV = "NVIDIA_DRIVER_ROOT"

// Default path where the GPU Operator's containerized driver creates device files.
const DefaultGPUOperatorDriverRoot = "/run/nvidia/driver"

// HostRoot returns the root path for accessing host filesystem
// Defaults to "/" if HOST_ROOT environment variable is not set
func HostRoot() string {
	if root, exists := os.LookupEnv(HOST_ROOT_ENV); exists {
		return root
	}
	return "/"
}

// ToHostPath joins the host root with the given path
func ToHostPath(path string) string {
	return filepath.Join(HostRoot(), path)
}

// NvidiaDriverRoot returns a custom NVIDIA driver root path if set via
// NVIDIA_DRIVER_ROOT. Returns empty string when unset.
func NvidiaDriverRoot() string {
	if root, exists := os.LookupEnv(NVIDIA_DRIVER_ROOT_ENV); exists {
		return root
	}
	return ""
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
