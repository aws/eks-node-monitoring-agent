package osext

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type Exec interface {
	Command(string, ...string) *exec.Cmd
}

func NewExec(root string) *execExt {
	return &execExt{root: root}
}

type execExt struct {
	root string
}

func (a *execExt) Command(name string, arg ...string) *exec.Cmd {
	proc := exec.Command(name, arg...)
	if a.root != "/" {
		if errors.Is(proc.Err, exec.ErrNotFound) {
			// if the binary could not be discovered using exec.LookPath then
			// search using our own implementation that takes the root path into
			// consideration. this can happen when the file on the host is a
			// symlink with an absolute path on the host, which is viewed as a
			// broken symlink and returned as a 'file not found'.
			// for example:
			// ---
			// lrwxrwxrwx 1 root root 26 Jul 20 01:33 /usr/sbin/iptables -> /etc/alternatives/iptables
			proc.Path, proc.Err = a.lookPath(name)
		}

		// trim off the root path because the executable path search is done
		// from the running container rather than the chrooted environment.
		// also see: https://github.com/golang/go/issues/39341
		proc.Path = strings.TrimPrefix(proc.Path, a.root)
		proc.SysProcAttr = &syscall.SysProcAttr{
			Chroot: a.root,
		}
	}
	return proc
}

// lookPath is an implemention of exec.lookPath which will resolve the names of
// files in the os PATH and optionally search the host root filesystem.
func (a *execExt) lookPath(file string) (string, error) {
	var searchPaths []string
	// if the path is just the filename, then we search the environment paths.
	// if the paths is relative or absolute, then we explicitly use those.
	if filepath.Base(file) == file {
		for _, path := range filepath.SplitList(os.Getenv("PATH")) {
			searchPaths = append(searchPaths,
				filepath.Join(path, file),
				filepath.Join(a.root, path, file),
			)
		}
	} else {
		searchPaths = append(searchPaths, file)
		// if the filepath is absolute, then we should also search host root.
		if filepath.IsAbs(file) {
			searchPaths = append(searchPaths, filepath.Join(a.root, file))
		}
	}
	// return the first match for the executable.
	for _, path := range searchPaths {
		if path, err := a.findExecutable(path); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func (a *execExt) findExecutable(path string) (string, error) {
	path, err := a.walkSymlinks(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", syscall.EISDIR
	}
	// we are skipping the Eaccess call that normally occurs here to
	// determine accessibility of the file, since we may as well try to
	// execute it regardless.

	// check the permissions
	if fi.Mode()&0111 != 0 {
		return path, nil
	}
	return "", fs.ErrPermission
}

// walkSymlinks returns the path after evaluating symlinks in all segments of
// the path string.
func (a *execExt) walkSymlinks(path string) (string, error) {
	var newPath string
	if filepath.IsAbs(path) {
		newPath = "/"
	}
	for _, part := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		curPath := filepath.Join(newPath, part)
		if fi, err := os.Lstat(curPath); err != nil {
			return "", err
		} else if fi.Mode()&os.ModeSymlink != 0 {
			// if this subpath contains a symlink, continue from that point.
			if newPath, err = a.followSymlink(curPath, fi); err != nil {
				return "", err
			}
		} else {
			newPath = curPath
		}
	}
	// if the path changed, we need to re-walk it in case it contains other
	// nested symlink subpaths.
	if path != newPath {
		return a.walkSymlinks(newPath)
	}
	return newPath, nil
}

// followSymlink returns the updated path using the symlink, taking into account
// if it is relative, or comes from the host filesystem.
func (a *execExt) followSymlink(path string, fi os.FileInfo) (string, error) {
	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(link) {
		// relative paths like '..' operate on the working directory, so if this
		// is a file we need to strip off it's name.
		if !fi.IsDir() {
			path = filepath.Dir(path)
		}
		return filepath.Clean(filepath.Join(path, link)), nil
	}
	// if the symlink is on a different root path, prepend before recursing.
	if a.root != "/" && strings.HasPrefix(path, a.root) && !strings.HasPrefix(link, a.root) {
		return filepath.Join(a.root, link), nil
	}
	return link, nil
}
