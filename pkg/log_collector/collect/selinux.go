package collect

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/sys/mountinfo"
)

type SELinux struct{}

var _ Collector = (*SELinux)(nil)

func (S SELinux) Collect(acc *Accessor) error {
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		// TODO: this doesn't work properly on bottlerocket host because the paths
		// used by the mountinfo package are not configured to search the host.
		res, err := getSELinux(acc)
		if err != nil {
			return err
		}
		return acc.WriteOutput("system/selinux.txt", res)
	}
	return nil
}

func getSELinux(acc *Accessor) ([]byte, error) {
	selinuxMounts, err := mountinfo.GetMounts(func(info *mountinfo.Info) (skip, stop bool) {
		return info.FSType != "selinux", false
	})
	if err != nil {
		return nil, fmt.Errorf("finding selinux mountpoint, %w", err)
	}
	if len(selinuxMounts) == 0 {
		// mimic'ing the existing log collector script
		return []byte("SELinux mode:\n\t Disabled (no mountpoint)\n"), nil
		// TODO: test output was 'Disabled' but eks-log-collector had
		// 'Permissive'. need to double check what is going on here.
	}
	mountPoint := selinuxMounts[0].Mountpoint
	contents, err := os.ReadFile(filepath.Join(acc.cfg.Root, mountPoint, "enforce"))
	if err != nil {
		return nil, fmt.Errorf("reading enforce file, %w", err)
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("empty enforce file")
	}
	if contents[0] == '0' {
		return []byte("SELinux mode:\n\t Permissive\n"), nil
	}
	return []byte("SELinux mode:\n\t Enforcing\n"), nil
}
