package file

import (
	"syscall"
)

// CheckDiskSpace returns the fraction of disk space used at the given path.
// Returns a value between 0.0 and 1.0, or an error if the path is invalid.
func CheckDiskSpace(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, nil
	}
	used := stat.Blocks - stat.Bavail
	return float64(used) / float64(stat.Blocks), nil
}
