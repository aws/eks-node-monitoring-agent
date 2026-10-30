package main

import (
	"slices"
	"testing"
)

func TestCommand(t *testing.T) {
	env := []string{"AWS_PROFILE=node", "KUBERNETES_EXEC_INFO={}"}
	args := []string{"eks", "get-token", "--cluster-name", "c"}

	t.Run("runs the plugin directly without systemd-run", func(t *testing.T) {
		name, got := command(false, env, "aws", args)
		if name != "aws" || !slices.Equal(got, args) {
			t.Fatalf("got %q %q", name, got)
		}
	})

	t.Run("runs the plugin as a transient unit with the caller's env", func(t *testing.T) {
		name, got := command(true, env, "aws", args)
		want := []string{"--pipe", "--wait", "--quiet", "--collect",
			"--setenv=AWS_PROFILE=node", "--setenv=KUBERNETES_EXEC_INFO={}",
			"--", "aws", "eks", "get-token", "--cluster-name", "c"}
		if name != systemdRun || !slices.Equal(got, want) {
			t.Fatalf("got %q %q", name, got)
		}
	})
}

func TestHostEnv(t *testing.T) {
	got := hostEnv([]string{"A=1", "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/host/run/dbus/system_bus_socket", "B=2"})
	if want := []string{"A=1", "B=2"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
