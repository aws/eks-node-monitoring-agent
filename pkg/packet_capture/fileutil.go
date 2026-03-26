package packet_capture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findGzipFiles reads the directory and returns only .gz files (full paths), sorted lexicographically.
func findGzipFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gz") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// findNonGzipFiles reads the directory and returns non-.gz files that match the base name pattern
// (e.g., capture.pcap, capture.pcap00001, etc.), full paths, sorted lexicographically.
func findNonGzipFiles(dir string, baseName string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".gz") && strings.HasPrefix(e.Name(), baseName) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// deleteCorrespondingPcapFile deletes the .pcap file corresponding to a .gz file path.
// Returns error if the input doesn't end with .gz. Returns nil if the .pcap file doesn't exist.
func deleteCorrespondingPcapFile(gzPath string) error {
	if !strings.HasSuffix(gzPath, ".gz") {
		return fmt.Errorf("expected .gz file, got: %s", gzPath)
	}
	pcapPath := strings.TrimSuffix(gzPath, ".gz")
	if err := os.Remove(pcapPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
