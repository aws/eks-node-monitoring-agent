package nvml

import (
	"os"
	"path/filepath"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

// libraryName is the soname every NVIDIA driver installs. go-nvml opens it
// with dlopen at Init, so the agent binary has no link-time dependency on it.
const libraryName = "libnvidia-ml.so.1"

// libraryDirs are where driver packages install libraryName, relative to a
// driver root, in the order NMA tries them. An entry may be a glob.
var libraryDirs = []string{
	"/usr/lib64",                 // RPM-based distributions, such as AL2023
	"/usr/lib/x86_64-linux-gnu",  // Debian and Ubuntu multiarch, x86_64
	"/usr/lib/aarch64-linux-gnu", // Debian and Ubuntu multiarch, arm64
	"/usr/share/.nvidia/*/lib64", // Bottlerocket, one root per driver branch (lts for R580, pb for R595)
}

// libraryCandidates returns the library paths to try, in order. Every path
// except the soname is under the host root, so a containerized agent always
// loads the host's copy, which matches the loaded kernel module.
func libraryCandidates() []string {
	var out []string
	if config.HostRoot() == "/" {
		// A host process (Auto Mode) lets the dynamic loader resolve the
		// soname, the same way nv-hostengine does.
		out = append(out, libraryName)
	}
	for _, root := range driverRoots() {
		for _, dir := range libraryDirs {
			// Glob returns only paths that exist. Its only error is a bad
			// pattern, which only an override path containing glob syntax can
			// produce; that root then yields no candidates.
			matches, _ := filepath.Glob(config.ToHostPath(filepath.Join(root, dir, libraryName)))
			out = append(out, matches...)
		}
	}
	return out
}

// driverRoots returns the driver roots to search, following the device-file
// lookup in package dcgm: an explicit override on its own; otherwise the GPU
// Operator root when present, then the host's root.
func driverRoots() []string {
	if override := config.NvidiaDriverRoot(); override != "" {
		return []string{override}
	}
	var roots []string
	if info, err := os.Stat(config.ToHostPath(config.DefaultGPUOperatorDriverRoot)); err == nil && info.IsDir() {
		roots = append(roots, config.DefaultGPUOperatorDriverRoot)
	}
	return append(roots, "/")
}

// openLibrary initializes NVML from the first candidate that dlopen accepts.
// It returns that library, its path, and the result of Init. go-nvml reports
// a failed dlopen as ERROR_LIBRARY_NOT_FOUND, which moves on to the next
// candidate; any other result means the library loaded, so the search stops.
// A nil library means no candidate loaded.
func openLibrary(candidates []string, newLibrary func(path string) gonvml.Interface) (gonvml.Interface, string, gonvml.Return) {
	for _, path := range candidates {
		lib := newLibrary(path)
		if ret := lib.Init(); ret != gonvml.ERROR_LIBRARY_NOT_FOUND {
			return lib, path, ret
		}
	}
	return nil, "", gonvml.ERROR_LIBRARY_NOT_FOUND
}
