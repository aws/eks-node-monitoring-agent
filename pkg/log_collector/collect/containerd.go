package collect

import (
	"errors"
	"path/filepath"
)

type Containerd struct{}

var _ Collector = (*CommonLogs)(nil)

func (c *Containerd) Collect(acc *Accessor) error {
	return errors.Join(
		ctrdlogs(acc),
		acc.CommandOutput([]string{"containerd", "config", "dump"}, "containerd/containerd-config.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-u", "containerd"}, "containerd/containerd-log.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "version"}, "containerd/containerd-version.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "namespaces", "list"}, "containerd/containerd-namespaces.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "--namespace", "k8s.io", "images", "list"}, "containerd/containerd-images.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "--namespace", "k8s.io", "containers", "list"}, "containerd/containerd-containers.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "--namespace", "k8s.io", "tasks", "list"}, "containerd/containerd-tasks.txt", CommandOptionsNone),
		acc.CommandOutput([]string{"ctr", "--namespace", "k8s.io", "plugins", "list"}, "containerd/containerd-plugins.txt", CommandOptionsNone),
	)
}

func ctrdlogs(acc *Accessor) error {
	containerLogs, err := filepath.Glob(filepath.Join(acc.cfg.Root, "/tmp/containerd.*.stacks.log"))
	if err != nil {
		return err
	}
	var merr error
	for _, logPath := range containerLogs {
		merr = errors.Join(merr, acc.CopyFile(logPath, filepath.Join("containerd", filepath.Base(logPath))))
	}
	return merr
}
