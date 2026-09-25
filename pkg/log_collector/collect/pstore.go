package collect

import (
	"errors"
	"os"
	"path/filepath"
)

// Pstore collects the contents of the kernel pstore backend and the
// systemd-pstore archive for kernel-panic debugging.
type Pstore struct{}

var _ Collector = (*Pstore)(nil)

// pstoreSource ties a host source directory to the destination it should be
// copied into and to the messages written when the source is missing or empty
type pstoreSource struct {
	// src is the absolute host path (before being reoriented under Config.Root)
	// to read pstore records from.
	src string
	// destDir is the destination subdirectory (relative to Config.Destination)
	// that pstore records are copied into when src is present and non-empty.
	destDir string
	// destNote is the destination file (relative to Config.Destination) that
	// receives an explanatory message when src is absent or empty. It is only
	// written when destDir would otherwise be empty, so the bundle never has
	// both a populated destDir and a destNote.
	destNote string
	// missingMsg explains that the pstore source is not present on the host,
	// which usually means the backend is not configured (or, for the systemd
	// archive, that systemd-pstore.service is not enabled).
	missingMsg string
	// emptyMsg explains that the pstore source is present but contains no
	// crash records, which is the expected steady state for a healthy host.
	emptyMsg string
}

var pstoreSources = []pstoreSource{
	{
		src:        "/sys/fs/pstore",
		destDir:    "pstore/sys_fs_pstore",
		destNote:   "pstore/sys_fs_pstore.txt",
		missingMsg: "/sys/fs/pstore is not present on this system (pstore backend may not be configured).\n",
		emptyMsg:   "/sys/fs/pstore is present but empty (no captured crash records).\n",
	},
	{
		src:        "/var/lib/systemd/pstore",
		destDir:    "pstore/systemd_pstore",
		destNote:   "pstore/systemd_pstore.txt",
		missingMsg: "/var/lib/systemd/pstore is not present on this system (systemd-pstore.service may not be enabled).\n",
		emptyMsg:   "/var/lib/systemd/pstore is present but empty (no archived crash records).\n",
	},
}

// Collect writes each pstore source into the bundle. Each source is captured
// independently so a failure or missing directory for one does not stop the
// other from being collected.
func (p *Pstore) Collect(acc *Accessor) error {
	var merr error
	for _, s := range pstoreSources {
		merr = errors.Join(merr, capturePstoreSource(acc, s))
	}
	return merr
}

// capturePstoreSource copies one pstore directory into the bundle, or writes an
// explanatory note when the directory is missing or empty.
func capturePstoreSource(acc *Accessor, s pstoreSource) error {
	src := filepath.Join(acc.cfg.Root, s.src)

	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Distinguishing missing from empty gives someone reading the bundle
			// enough to tell whether the pstore backend or service is configured
			// at all, versus configured but idle.
			return acc.WriteOutput(s.destNote, []byte(s.missingMsg))
		}
		return err
	}
	if !info.IsDir() {
		// Neither /sys/fs/pstore nor /var/lib/systemd/pstore should ever be a
		// non-directory
		return acc.WriteOutput(s.destNote, []byte(s.src+" exists but is not a directory.\n"))
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return acc.WriteOutput(s.destNote, []byte(s.emptyMsg))
	}
	// CopyDir preserves each entry's original filename, which matters for
	// pstore records: the kernel encodes the panic count, backend, and part
	// number into the name (e.g. dmesg-efi-165000000000000001), and renaming
	// would drop that information.
	return acc.CopyDir(src, s.destDir)
}
