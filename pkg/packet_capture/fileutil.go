package packet_capture

import (
	"compress/gzip"
	"fmt"
	"io"
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

// gzipFile compresses a file with gzip, writing to path.gz, then deletes the original file.
// On error, removes any incomplete .gz file to prevent uploading corrupt data.
func gzipFile(path string) (err error) {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	gzPath := path + ".gz"
	dst, err := os.Create(gzPath)
	if err != nil {
		return err
	}

	gw := gzip.NewWriter(dst)

	defer func() {
		if gw != nil {
			if closeErr := gw.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
		if dst != nil {
			if closeErr := dst.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
		if err != nil {
			_ = os.Remove(gzPath)
		}
	}()

	if _, err = io.Copy(gw, src); err != nil {
		return err
	}

	// Close gzip writer to flush, then close dst, then remove original
	if err = gw.Close(); err != nil {
		return err
	}
	gw = nil // prevent double close in defer
	if err = dst.Close(); err != nil {
		return err
	}
	dst = nil // prevent double close in defer
	src.Close()
	return os.Remove(path)
}
