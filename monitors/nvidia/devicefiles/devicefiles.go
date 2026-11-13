// Package devicefiles counts the NVIDIA GPU device files on the host. Both
// GPU readers compare their GPU count against it.
package devicefiles

import (
	"os"
	"path/filepath"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

// dirs returns the candidate directories to search for nvidia device files,
// ordered by priority: explicit override, GPU Operator default, /dev.
func dirs() []string {
	if root := config.NvidiaDriverRoot(); root != "" {
		return []string{config.ToHostPath(filepath.Join(root, "dev"))}
	}
	gpuOpDir := config.ToHostPath(filepath.Join(config.DefaultGPUOperatorDriverRoot, "dev"))
	standardDir := config.ToHostPath("/dev")
	if info, err := os.Stat(gpuOpDir); err == nil && info.IsDir() {
		return []string{gpuOpDir, standardDir}
	}
	return []string{standardDir}
}

// Count globs the candidate directories and returns the count from whichever
// directory contains the most nvidia device files, and that directory.
func Count() (uint, string, error) {
	var bestCount uint
	var bestDir string
	for _, dir := range dirs() {
		paths, err := filepath.Glob(filepath.Join(dir, "nvidia[0-9]*"))
		if err != nil {
			return 0, dir, err
		}
		if count := uint(len(paths)); count > bestCount {
			bestCount = count
			bestDir = dir
		}
	}
	if bestDir == "" {
		candidates := dirs()
		bestDir = candidates[len(candidates)-1]
	}
	return bestCount, bestDir, nil
}
