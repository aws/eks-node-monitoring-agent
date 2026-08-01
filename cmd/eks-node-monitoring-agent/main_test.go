package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestRegisteredCheck(t *testing.T) {
	t.Run("not ready while registration is in progress", func(t *testing.T) {
		if err := registeredCheck(make(chan struct{}))(nil); err == nil {
			t.Fatal("expected an error before registration completes")
		}
	})

	t.Run("ready once registration completes", func(t *testing.T) {
		registered := make(chan struct{})
		close(registered)
		if err := registeredCheck(registered)(nil); err != nil {
			t.Fatalf("unexpected error after registration: %v", err)
		}
	})
}

// TestHealthProbesServeWhileStartupRunnableBlocks pins the behaviour the startup
// Runnable relies on: the manager serves /healthz before it starts Runnables, so a
// Runnable that blocks indefinitely (as the node bootstrap poll can) cannot leave
// the liveness probe unanswered. /readyz must report unready for that same window,
// then ready once registration completes — deliberately while the Runnable is still
// executing, since the Runnable ends in a monitor loop that never returns.
func TestHealthProbesServeWhileStartupRunnableBlocks(t *testing.T) {
	probeAddr := freeLocalAddr(t)

	mgr, err := controllerruntime.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, controllerruntime.Options{
		Scheme:                 scheme.Scheme,
		HealthProbeBindAddress: probeAddr,
		Metrics:                server.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	registered := make(chan struct{})
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		t.Fatalf("failed to add healthz check: %v", err)
	}
	if err := mgr.AddReadyzCheck("readyz", registeredCheck(registered)); err != nil {
		t.Fatalf("failed to add readyz check: %v", err)
	}

	// Stands in for the startup Runnable while it is still blocked on the API server.
	blocking := make(chan struct{})
	if err := mgr.Add(crmanager.RunnableFunc(func(ctx context.Context) error {
		close(blocking)
		<-ctx.Done()
		return nil
	})); err != nil {
		t.Fatalf("failed to add runnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- mgr.Start(ctx) }()

	select {
	case <-blocking:
	case err := <-startErr:
		t.Fatalf("manager returned before the runnable started: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the runnable to start")
	}

	// Registration is deliberately still in flight here.
	if code := probe(t, probeAddr, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want %d while registration is in progress", code, http.StatusOK)
	}
	// healthz reports a failing check as 500, which kubelet counts as a failure.
	if code := probe(t, probeAddr, "/readyz"); code != http.StatusInternalServerError {
		t.Fatalf("/readyz = %d, want %d while registration is in progress", code, http.StatusInternalServerError)
	}

	// The Runnable is still executing, mirroring the monitor loop that follows
	// registration in run(): readiness is scoped to registration, not to that loop.
	close(registered)
	if code := probe(t, probeAddr, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d, want %d after registration", code, http.StatusOK)
	}

	cancel()
	if err := <-startErr; err != nil {
		t.Fatalf("manager returned an error: %v", err)
	}
}

func probe(t *testing.T, addr, path string) int {
	t.Helper()
	// The probe server binds during manager construction but only serves once
	// Start reaches it, so retry briefly rather than racing the first request.
	var lastErr error
	for range 50 {
		resp, err := http.Get(fmt.Sprintf("http://%s%s", addr, path))
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	t.Fatalf("failed to reach %s: %v", path, lastErr)
	return 0
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a local port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("failed to release the reserved port: %v", err)
	}
	return addr
}
