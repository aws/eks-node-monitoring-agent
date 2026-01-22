package config

import (
	"os"
	"path/filepath"
)

const HOST_ROOT_ENV = "HOST_ROOT"

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
