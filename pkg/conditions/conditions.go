// Package conditions defines standard Kubernetes node condition types
// used by the EKS Node Monitoring Agent.
//
// These condition types are used to report the health status of various
// node subsystems to the Kubernetes API server as NodeConditions.
package conditions

import (
	corev1 "k8s.io/api/core/v1"
)

const (
	// AcceleratedHardwareReady indicates whether accelerated hardware (GPU, Neuron)
	// on the node is functioning correctly.
	AcceleratedHardwareReady corev1.NodeConditionType = "AcceleratedHardwareReady"

	// ContainerRuntimeReady indicates whether the container runtime (containerd, etc.)
	// is functioning correctly and able to run containers.
	ContainerRuntimeReady corev1.NodeConditionType = "ContainerRuntimeReady"

	// DiskPressure is a standard Kubernetes condition indicating the node is
	// experiencing disk pressure (low disk space or high I/O).
	DiskPressure corev1.NodeConditionType = "DiskPressure"

	// KernelReady indicates whether the kernel is functioning correctly
	// without critical errors, panics, or resource exhaustion.
	KernelReady corev1.NodeConditionType = "KernelReady"

	// MemoryPressure is a standard Kubernetes condition indicating the node is
	// experiencing memory pressure (low available memory).
	MemoryPressure corev1.NodeConditionType = "MemoryPressure"

	// NetworkingReady indicates whether the node's networking stack is
	// functioning correctly (interfaces, routing, connectivity).
	NetworkingReady corev1.NodeConditionType = "NetworkingReady"

	// Ready is the standard Kubernetes condition indicating the node is
	// healthy and ready to accept pods.
	Ready corev1.NodeConditionType = "Ready"

	// StorageReady indicates whether the node's storage subsystem is
	// functioning correctly (disks, filesystems, I/O).
	StorageReady corev1.NodeConditionType = "StorageReady"
)
