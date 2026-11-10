// chroot wrapper written using go because we dont have chroot on the eks node
// monitoring agent docker image.
//
// client-go runs the kubeconfig's exec credential plugin through this wrapper.
// The plugin (e.g. `aws eks get-token`) can use ~80MiB while it runs, and as a
// child of the agent that memory is charged to the agent's container cgroup,
// which is enough to get the agent OOM killed on every token refresh. When the
// host has systemd-run, the plugin is started as a transient systemd unit
// instead, so its memory is charged to that unit and not to the agent.

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

const systemdRun = "/usr/bin/systemd-run"

func main() {
	hostRoot := os.Args[1]
	if _, err := os.Stat(filepath.Join(hostRoot, systemdRun)); err == nil {
		env := hostEnv(os.Environ())
		name, args := command(true, env, os.Args[2], os.Args[3:])
		if run(hostRoot, env, name, args) == nil {
			return
		}
		// if systemd cannot run the plugin (or the plugin itself failed) run it
		// directly, so the worst case is the behavior from before systemd-run.
	}
	name, args := command(false, nil, os.Args[2], os.Args[3:])
	if err := run(hostRoot, nil, name, args); err != nil {
		panic(err)
	}
}

func run(hostRoot string, env []string, name string, args []string) error {
	cmd := osext.NewExec(hostRoot).Command(name, args...)
	cmd.Env = env // nil inherits our environment
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// hostEnv drops DBUS_SYSTEM_BUS_ADDRESS. The agent points it at the host's bus
// through the host root mount, a path that does not exist once chrooted, and
// systemd-run fails with "Failed to connect to bus" when it falls back to it.
func hostEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "DBUS_SYSTEM_BUS_ADDRESS=") {
			out = append(out, kv)
		}
	}
	return out
}

// command returns the command line that runs name with args on the host. With
// systemd-run, the plugin runs as a transient unit outside the agent's cgroup.
// A unit does not inherit the caller's environment, so env is passed through
// explicitly: it carries the kubeconfig's exec env and KUBERNETES_EXEC_INFO,
// which client-go sets for the plugin.
func command(useSystemdRun bool, env []string, name string, args []string) (string, []string) {
	if !useSystemdRun {
		return name, args
	}
	// --pipe connects the unit's stdio to ours so client-go reads the credential
	// from stdout; --wait propagates the plugin's exit code; --collect removes
	// the unit even when the plugin fails.
	runArgs := []string{"--pipe", "--wait", "--quiet", "--collect"}
	for _, kv := range env {
		runArgs = append(runArgs, "--setenv="+kv)
	}
	return systemdRun, append(append(runArgs, "--", name), args...)
}
