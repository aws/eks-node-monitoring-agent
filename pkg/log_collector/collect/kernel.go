package collect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Kernel struct{}

var _ Collector = (*Kernel)(nil)

var defaultModules []string = []string{
	"lustre",
}

// watchedLoadedModules are matched against lsmod output: ip_vs for kube-proxy
// IPVS mode and nf_conntrack for connection tracking state. lsmod shows what
// is actually loaded (and its refcounts), which modinfo cannot.
var watchedLoadedModules = []string{"ip_vs", "nf_conntrack"}

func (k *Kernel) Collect(acc *Accessor) error {
	return errors.Join(
		dmesg(acc),
		modinfo(acc),
		loadedModules(acc),
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

// loadedModules writes the lsmod lines for the watched modules to
// modinfo/ip_vs, matching the path the log collector script uses. The file is
// written even when no watched module is loaded, so "nothing loaded" is
// distinguishable from "not collected".
func loadedModules(acc *Accessor) error {
	output, err := acc.CombinedOutput("lsmod")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, line := range strings.Split(string(output), "\n") {
		for _, mod := range watchedLoadedModules {
			if strings.Contains(line, mod) {
				buf.WriteString(line + "\n")
				break
			}
		}
	}
	return acc.WriteOutput("modinfo/ip_vs", buf.Bytes())
}
