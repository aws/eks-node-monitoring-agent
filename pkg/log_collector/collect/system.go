package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/system"
)

type System struct{}

var _ Collector = (*System)(nil)

func (s System) Collect(acc *Accessor) error {
	return errors.Join(
		top(acc),
		systemd(acc),
		ps(acc),
		procs(acc),
		sysctl(acc),
		pkgs(acc),
		networking(acc),
		bottlerocket(acc),
		reboots(acc),
		largeEnvironments(acc),
	)
}

// largeEnvironmentThreshold is the environment variable count above which a
// process is reported. Kubernetes service-link injection in namespaces with
// thousands of services produces environments this large, which cause exec
// failures and kubelet slowness.
const largeEnvironmentThreshold = 1000

// largeEnvironments writes the PIDs whose environment holds more than
// largeEnvironmentThreshold variables. Unreadable environ files (processes of
// other users, or ones that exited mid-scan) are skipped.
func largeEnvironments(acc *Accessor) error {
	environPaths, err := filepath.Glob(filepath.Join(acc.cfg.Root, "/proc/[0-9]*/environ"))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("PID\tCount\n")
	for _, environPath := range environPaths {
		data, err := os.ReadFile(environPath)
		if err != nil {
			continue
		}
		count := 0
		for _, entry := range bytes.Split(data, []byte{0}) {
			if len(entry) > 0 {
				count++
			}
		}
		if count > largeEnvironmentThreshold {
			pid := filepath.Base(filepath.Dir(environPath))
			buf.WriteString(fmt.Sprintf("%s\t%d\n", pid, count))
		}
	}
	return acc.WriteOutput("system/large_environments.txt", buf.Bytes())
}

func top(acc *Accessor) error {
	var topOutput bytes.Buffer
	if err := system.Top(&topOutput, acc.cfg.Root); err != nil {
		return err
	}
	return acc.WriteOutput("system/top.txt", topOutput.Bytes())
}

func networking(acc *Accessor) error {
	var merr error
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		merr = errors.Join(merr, acc.CommandOutput([]string{"netstat", "-plant"}, "system/netstat.txt", CommandOptionsNone))
	}
	return merr
}

func systemd(acc *Accessor) error {
	var merr error
	// TODO: need to root cause the 'Access Denied' errors when calling these
	// functions. normally this could happend when SELinux kicks in, but there
	// were no audits for these commands last time i checked.
	if !acc.cfg.hasAnyTag(TagBottlerocket) || acc.cfg.hasAnyTag(TagEKSAuto) {
		merr = errors.Join(merr,
			acc.CommandOutput([]string{"systemctl", "list-units"}, "system/services.txt", CommandOptionsNone),
			acc.CommandOutput([]string{"systemd-analyze", "plot"}, "system/systemd-analyze.svg", CommandOptionsNone),
		)
	}
	return merr
}

func ps(acc *Accessor) error {
	return errors.Join(
		acc.CommandOutput([]string{"ps", "fauxwww", "--headers"}, "system/ps.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ps", "-eTF", "--headers"}, "system/ps-threads.txt", CommandOptionsNone),
	)
}

func procs(acc *Accessor) error {
	kernelProcStat := func() error {
		return acc.CopyFile(filepath.Join(acc.cfg.Root, "/proc/stat"), "system/procstat.txt")
	}
	userProcStat := func() error {
		statPaths, err := filepath.Glob(filepath.Join(acc.cfg.Root, "/proc/[0-9]*/stat"))
		if err != nil {
			return err
		}
		var merr error
		var buf bytes.Buffer
		for _, statPath := range statPaths {
			f, err := os.Open(statPath)
			if err != nil {
				merr = errors.Join(merr, err)
				continue
			}
			defer f.Close()
			if _, err = io.Copy(&buf, f); err != nil {
				merr = errors.Join(merr, err)
			}
		}
		return acc.WriteOutput("system/allprocstat.txt", buf.Bytes())
	}

	return errors.Join(
		kernelProcStat(),
		userProcStat(),
	)
}

func sysctl(acc *Accessor) error {
	// TODO(pod): output is different from instance
	output, err := acc.CombinedOutput("sysctl", "--all")
	// since procps 4.0.7, 'sysctl --all' exits 1 when any single key cannot be
	// read, even though every other key was printed. the unset ipv6
	// stable_secret keys fail with EIO on every host, so this is the steady
	// state rather than a failed capture: keep the dump and do not report it.
	// see https://gitlab.com/procps-ng/procps/-/commit/6860675ae33420741c5264785becc21ea15439d4
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(output) > 0 {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("executing command %q: %w", "sysctl --all", err)
	}
	return acc.WriteOutput("sysctls/sysctl_all.txt", output)
}

func pkgs(acc *Accessor) error {
	if acc.cfg.hasAnyTag(TagBottlerocket) {
		return nil
	}
	// the expectation is that at least one of these is expected to succeed
	// depending on the underyling OS distro.
	rpmErr := acc.CommandOutput([]string{"rpm", "-qa"}, "system/pkglist.txt", CommandOptionsNone)
	debErr := acc.CommandOutput([]string{"deb", "--list"}, "system/pkglist.txt", CommandOptionsNone)
	if rpmErr == nil || debErr == nil {
		return nil
	}
	return errors.Join(rpmErr, debErr)
}

func reboots(acc *Accessor) error {
	var merr error
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		merr = errors.Join(merr, acc.CommandOutput([]string{"last", "reboot"}, "system/last_reboot.txt", CommandOptionsNone))
	}
	return merr
}

// this could live in its own collector if there is lots of data to collect
// specifically for bottlerocket, but just adding this to keep track.
func bottlerocket(acc *Accessor) error {
	if !acc.cfg.hasAnyTag(TagBottlerocket) {
		return nil
	}

	return errors.Join(
		acc.CopyFile(filepath.Join(acc.cfg.Root, "/usr/share/bottlerocket/application-inventory.json"), "bottlerocket/application-inventory.json"),
		// Logdog command does not print any logs itself but instead collects the logs from various sources and stores it into a tarball.
		// We store the output of the logdog command invocation in command-output.log
		acc.CommandOutput([]string{"logdog"}, "bottlerocket/logdog/command-output.log", CommandOptionsNone),
		// This copies the actual logdog tarball to a required location.
		acc.CopyFile(filepath.Join(acc.cfg.Root, "/var/log/support/bottlerocket-logs.tar.gz"), "bottlerocket/logdog/bottlerocket-logs.tar.gz"),
	)
}
