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

// GzipFile compresses a file with gzip, writing to path.gz, then deletes the original file.
// On error, removes any incomplete .gz file to prevent uploading corrupt data.
func GzipFile(path string) (err error) {
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
	return os.Remove(path)
}

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
