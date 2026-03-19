# storage/

Disk layout, mount points, inode usage, XFS filesystem details, and per-pod overlay storage consumption.

**Collector source:** [`pkg/log_collector/collect/disk.go`](../../../pkg/log_collector/collect/disk.go)

Uses [`github.com/moby/sys/mountinfo`](https://pkg.go.dev/github.com/moby/sys/mountinfo) to enumerate XFS and overlay mounts by reading `/proc/self/mountinfo`.

---

## Files

### `mounts.txt`

Combined output of active mount table and disk space usage.

- **Commands:** `mount` then `df --human-readable` (appended)
- **Linux syscall:** `open(2)` on `/proc/self/mounts` (for `mount`); `statfs(2)` on each mountpoint (for `df`)
- **Content:** Two sections:
  1. All active mounts with filesystem type and mount options
  2. Human-readable disk space per filesystem (Size, Used, Avail, Use%)

**Sample output (truncated):**
```
/dev/dm-0 on / type erofs (ro,relatime,seclabel,user_xattr,acl,cache_strategy=readaround)
/dev/nvme1n1p1 on /local type xfs (rw,nosuid,nodev,noatime,seclabel,attr2,inode64,...)
/dev/nvme0n1p3 on /boot type ext4 (ro,nosuid,nodev,noexec,noatime,seclabel)
overlay on /run/containerd/io.containerd.runtime.v2.task/k8s.io/<id>/rootfs type overlay (rw,...)

Filesystem      Size  Used Avail Use% Mounted on
/dev/root       412M  412M     0 100% /
/dev/nvme1n1p1   80G  2.9G   78G   4% /local
/dev/nvme0n1p3   25M  9.4M   14M  41% /boot
```

Note: `/dev/root` at 100% is normal for Bottlerocket — the root filesystem is a read-only `erofs` image.

---

### `inodes.txt`

Inode usage per filesystem.

- **Command:** `df --inodes`
- **Linux syscall:** `statfs(2)` on each mountpoint (returns `f_files` and `f_ffree`)
- **Content:** Filesystem, total inodes, used inodes, free inodes, use%, mountpoint

**Sample output (truncated):**
```
Filesystem       Inodes IUsed    IFree IUse% Mounted on
/dev/root             -     - 18446744073709540218     - /
devtmpfs         985287   463   984824    1% /dev
/dev/nvme1n1p1 41942016 15302 41926714    1% /local
tmpfs            993775  1225   817975    1% /run
```

Inode exhaustion (`IUse% = 100%`) prevents new file creation even when disk space is available.

---

### `lsblk.txt`

Block device tree showing disk layout and mount points.

- **Command:** `lsblk`
- **Linux syscall:** `open(2)` on `/sys/block/` and `/proc/partitions`; `ioctl(2)` with `BLKGETSIZE64` for device sizes
- **Content:** Device name, major:minor numbers, removable flag, size, read-only flag, type (disk/part), and mountpoints

**Sample output:**
```
NAME        MAJ:MIN RM  SIZE RO TYPE MOUNTPOINTS
zram0       251:0    0    1G  0 disk [SWAP]
nvme0n1     259:0    0    4G  0 disk
├─nvme0n1p1 259:2    0    4M  0 part
├─nvme0n1p3 259:4    0  160M  0 part /boot
├─nvme0n1p7 259:8    0   89M  0 part /var/lib/bottlerocket
nvme1n1     259:1    0   80G  0 disk
└─nvme1n1p1 259:11   0   80G  0 part /var
                                      /opt
                                      /mnt
                                      /local
```

---

### `xfs.txt`

XFS filesystem geometry and free space distribution for all XFS-mounted volumes.

- **Commands:** `xfs_info <device>` and `xfs_db -r -c "freesp -s" <device>` (appended per XFS mount)
- **Source:** XFS mounts discovered via `mountinfo.GetMounts()` filtering on `FSType == "xfs"`
- **Linux syscall:** `open(2)` on `/proc/self/mountinfo`; `ioctl(2)` with XFS-specific ioctls for `xfs_info`
- **Content:** Filesystem geometry (block size, AG count, inode size) and a histogram of free extent sizes

**Sample output:**
```
meta-data=/dev/nvme1n1p1         isize=512    agcount=16, agsize=1310688 blks
         =                       crc=1        finobt=1, sparse=1, rmapbt=1
data     =                       bsize=4096   blocks=20971008, imaxpct=25
log      =internal log           bsize=4096   blocks=16384, version=2
   from      to extents   blocks    pct blkcdf extcdf
      1       1     130      130   0.00 100.00 100.00
1048576 1310688      16 20617873 100.00 100.00   9.64
total free extents 166
total free blocks 20618715
average free extent size 124209
```

---

### `pod_local_storage.txt`

Disk usage of each container's writable overlay layer (`upperdir`).

- **Source:** [`disk.go`](../../../pkg/log_collector/collect/disk.go) — enumerates overlay mounts via `mountinfo.GetMounts()`, extracts `upperdir=` from VFS options, runs `du -sh` on each
- **Linux syscall:** `open(2)` on `/proc/self/mountinfo`; `getdents64(2)` + `stat(2)` for `du`
- **Content:** Human-readable size and path for each container's writable layer. Containers that have written no data show `0`.

**Sample output:**
```
0	/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/2/fs
0	/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/3/fs
120K	/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/13/fs
0	/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/27/fs
```

Snapshot directories map to running containers. A large value here indicates a container writing significant data to its local filesystem.

---

### `fstab.txt`

Static filesystem table.

- **Source:** File copy of `/etc/fstab`
- **Linux syscall:** `open(2)`, `read(2)`
- **Content:** Configured mount points, filesystem types, and mount options
- **Not collected on:** Bottlerocket (fstab is not used)

---

### `lvs.txt`, `pvs.txt`, `vgs.txt`

LVM logical volumes, physical volumes, and volume groups.

- **Commands:** `lvs`, `pvs`, `vgs`
- **Linux syscall:** `ioctl(2)` on `/dev/mapper/control` (device-mapper)
- **Content:** LVM configuration if present; failures are silently ignored
- **Not collected on:** Bottlerocket
