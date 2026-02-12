package collect

import (
	"errors"
	"os"
	"path/filepath"
)

type Kernel struct{}

var _ Collector = (*Kernel)(nil)

var defaultModules []string = []string{
	"lustre",
}

func (k *Kernel) Collect(acc *Accessor) error {
	return errors.Join(
		dmesg(acc),
		modinfo(acc),
	)
}

func dmesg(acc *Accessor) error {
	var merr error
	// NOTE: this directory is a no-op on bottlerocket. calls to dmesg below are sufficient.
	// see: https://github.com/aws-samples/amazon-cloudwatch-container-insights/issues/136
	if stat, err := os.Stat(filepath.Join(acc.cfg.Root, "/var/log/dmesg")); err == nil && !stat.IsDir() {
		merr = errors.Join(merr, acc.CopyFile(filepath.Join(acc.cfg.Root, "/var/log/dmesg"), "kernel/dmesg.boot"))
	}
	return errors.Join(merr,
		acc.CommandOutput([]string{"dmesg"}, "kernel/dmesg.current", CommandOptionsNone),
		acc.CommandOutput([]string{"dmesg", "--ctime"}, "kernel/dmesg.human.current", CommandOptionsNone),
		acc.CommandOutput([]string{"uname", "-a"}, "kernel/uname.txt", CommandOptionsNone),
	)
}

func modinfo(acc *Accessor) error {
	var merr error
	for _, mod := range defaultModules {
		// modules are not consistent across distros, so avoid producing an
		// error here. we will assume this means the module does not exist.
		merr = errors.Join(merr, acc.CommandOutput([]string{"modinfo", mod}, filepath.Join("modinfo/", mod), CommandOptionsIgnoreFailure))
	}
	return merr
}
