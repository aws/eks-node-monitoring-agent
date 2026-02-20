package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TarGzipDir creates an in-memory gzip compressed tar archive by walking a provided
// directory path and trimming the directory prefix from the file paths.
func TarGzipDir(dir string) (io.Reader, error) {
	var archiveData bytes.Buffer
	gw := gzip.NewWriter(&archiveData)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	if err := filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		// skip directories
		if info.IsDir() {
			return nil
		}
		header, err := tar.FileInfoHeader(info, path)
		if err != nil {
			return err
		}
		// remove the collection directory prefix
		header.Name = strings.TrimPrefix(path, dir+string(filepath.Separator))
		if err = tw.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err = io.Copy(tw, file); err != nil {
			return err
		}
		return file.Close()
	}); err != nil {
		return nil, err
	}

	return &archiveData, nil
}

// EnsureParentExists creates the parent directory of a file if it doesn't exist
func EnsureParentExists(file string, perm fs.FileMode) error {
	root := filepath.Dir(file)
	f, err := os.Open(root)
	defer f.Close()
	if err == nil {
		// already exists
		return nil
	}
	return os.MkdirAll(root, perm)
}
