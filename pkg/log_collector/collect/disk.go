package collect

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/moby/sys/mountinfo"
)

var _ Collector = (*Disk)(nil)

type Disk struct{}

func (m Disk) Collect(acc *Accessor) error {
	var merr error

	merr = errors.Join(merr,
		acc.CommandOutput([]string{"mount"}, "storage/mounts.txt", CommandOptionsAppend),
		acc.CommandOutput([]string{"df", "--human-readable"}, "storage/mounts.txt", CommandOptionsAppend),
		acc.CommandOutput([]string{"df", "--inodes"}, "storage/inodes.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"lsblk"}, "storage/lsblk.txt", CommandOptionsNone),
	)

	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		merr = errors.Join(merr,
			// see: https://github.com/bottlerocket-os/bottlerocket-core-kit/blob/903b9f78972343587060fd29686660837bd09104/sources/xfscli/src/bin/fsck_xfs/main.rs#L72-L74
			acc.CopyFile(filepath.Join(acc.cfg.Root, "/etc/fstab"), "storage/fstab.txt"),

			acc.CommandOutput([]string{"lvs"}, "storage/lvs.txt", CommandOptionsIgnoreFailure),
			acc.CommandOutput([]string{"pvs"}, "storage/pvs.txt", CommandOptionsIgnoreFailure),
			acc.CommandOutput([]string{"vgs"}, "storage/vgs.txt", CommandOptionsIgnoreFailure),
		)
	}

	xfsInfo, err := mountinfo.GetMounts(func(info *mountinfo.Info) (skip, stop bool) {
		return info.FSType != "xfs", false
	})
	if err != nil {
		return errors.Join(merr, err)
	}
	uniqueXfsInfo := map[string]*mountinfo.Info{}
	for _, xfs := range xfsInfo {
		uniqueXfsInfo[xfs.Source] = xfs
	}
	for xfs := range uniqueXfsInfo {
		merr = errors.Join(merr, acc.CommandOutput([]string{"xfs_info", xfs}, "storage/xfs.txt", CommandOptionsAppend|CommandOptionsNoStderr))
		merr = errors.Join(merr, acc.CommandOutput([]string{"xfs_db", "-r", "-c", "freesp -s", xfs}, "storage/xfs.txt", CommandOptionsAppend|CommandOptionsNoStderr))
	}

	// pod files are stored in overlays, so we collect their storage to identify pods that are consuming lots of space
	overlayMounts, err := mountinfo.GetMounts(func(info *mountinfo.Info) (skip, stop bool) {
		return info.FSType != "overlay", false
	})
	if err != nil {
		return errors.Join(merr, err)
	}

	// find and record the local storage that has been used by pods
	for _, mount := range overlayMounts {
		// rw,lowerdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/1/fs,upperdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/33/fs,workdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/33/work
		options := strings.Split(mount.VFSOptions, ",")
		for _, opt := range options {
			// only interested in the upperdir which is where the pod written files are located
			if !strings.HasPrefix(opt, "upperdir=") {
				continue
			}
			dirPath := strings.TrimPrefix(opt, "upperdir=")
			merr = errors.Join(merr, acc.CommandOutput([]string{"du", "-sh", dirPath}, "storage/pod_local_storage.txt", CommandOptionsAppend))
		}
	}

	return merr
}
