package collect

import (
	"errors"
	"path/filepath"
)

type Nodeadm struct{}

// udevNetManagerCacheDir holds one directory per instance id, containing one
// file per network interface that nodeadm's udev-net-manager has handled.
// see: https://github.com/awslabs/amazon-eks-ami/blob/main/nodeadm/cmd/nodeadm-internal/udev/broker.go
const udevNetManagerCacheDir = "/etc/eks/nodeadm/udev-net-manager"

func (m Nodeadm) Collect(acc *Accessor) error {
	return errors.Join(
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "nodeadm-config"}, "nodeadm/nodeadm-config.log", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "nodeadm-run"}, "nodeadm/nodeadm-run.log", CommandOptionsNone),
		// the boot-hook unit only exists on AL2023 AMIs with nodeadm boot
		// hooks; an empty journal on other hosts is harmless.
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "nodeadm-boot-hook"}, "nodeadm/nodeadm-boot-hook.log", CommandOptionsNone),
		udevNetManagerJournals(acc),
	)
}

// udevNetManagerJournals collects the per-interface udev-net-manager@<iface>
// unit journals for every interface cached under udevNetManagerCacheDir, which
// is where multi-NIC interface bring-up failures on AL2023 are recorded. A
// missing cache directory means the host does not use udev-net-manager.
func udevNetManagerJournals(acc *Accessor) error {
	ifaces, err := cachedUdevInterfaces(acc)
	if err != nil {
		return err
	}
	return collectUdevJournals(acc, ifaces)
}

// cachedUdevInterfaces lists the interface names cached under every
// <instance-id> directory in udevNetManagerCacheDir, deduplicated.
func cachedUdevInterfaces(acc *Accessor) ([]string, error) {
	interfaceFiles, err := filepath.Glob(filepath.Join(acc.cfg.Root, udevNetManagerCacheDir, "*", "*"))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var ifaces []string
	for _, interfaceFile := range interfaceFiles {
		iface := filepath.Base(interfaceFile)
		if seen[iface] {
			continue
		}
		seen[iface] = true
		ifaces = append(ifaces, iface)
	}
	return ifaces, nil
}

func collectUdevJournals(acc *Accessor, ifaces []string) error {
	var merr error
	for _, iface := range ifaces {
		merr = errors.Join(merr, acc.CommandOutput(
			[]string{"journalctl", "-o", "short-iso-precise", "-u", "udev-net-manager@" + iface},
			filepath.Join("nodeadm/", "udev-net-manager_"+iface+".log"),
			CommandOptionsNone,
		))
	}
	return merr
}
