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

// libraryDirs are the directories under a driver root where driver packages
// install libraryName: RPM-based distributions first, then Debian multiarch.
var libraryDirs = []string{"/usr/lib64", "/usr/lib/x86_64-linux-gnu", "/usr/lib/aarch64-linux-gnu"}

// bottlerocketLibraryGlob matches the Bottlerocket NVIDIA kernel module
// packages, which install the library under a per-branch root (lts for R580,
// pb for R595).
// ref: https://github.com/bottlerocket-os/bottlerocket-kernel-kit (packages/kmod-*-nvidia-*/*.spec, nvidia_libdir)
const bottlerocketLibraryGlob = "/usr/share/.nvidia/*/lib64/" + libraryName

// libraryCandidates returns the library paths to try, in order. A host
// process (HOST_ROOT unset, as on Auto Mode) tries the soname first, so the
// dynamic loader finds the library the same way nv-hostengine does. Every
// other candidate is under the host root, so a containerized agent always
// loads the host's copy, which matches the loaded kernel module.
func libraryCandidates() []string {
	var out []string
	if config.HostRoot() == "/" {
		out = append(out, libraryName)
	}

	// Driver roots follow the device-file lookup in package dcgm: an explicit
	// override is used alone; otherwise the GPU Operator root, when present,
	// comes before the host's own root.
	var roots []string
	override := config.NvidiaDriverRoot()
	if override != "" {
		roots = []string{override}
	} else {
		if info, err := os.Stat(config.ToHostPath(config.DefaultGPUOperatorDriverRoot)); err == nil && info.IsDir() {
			roots = append(roots, config.DefaultGPUOperatorDriverRoot)
		}
		roots = append(roots, "/")
	}
	for _, root := range roots {
		for _, dir := range libraryDirs {
			if p := config.ToHostPath(filepath.Join(root, dir, libraryName)); isFile(p) {
				out = append(out, p)
			}
		}
	}
	if override == "" {
		// The pattern is a constant, so Glob cannot return ErrBadPattern.
		matches, _ := filepath.Glob(config.ToHostPath(bottlerocketLibraryGlob))
		out = append(out, matches...)
	}
	return out
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
