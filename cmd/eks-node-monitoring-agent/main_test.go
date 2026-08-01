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

// TestHealthProbesServeWhileStartupRunnableBlocks pins the guarantee the startup
// Runnable relies on: the manager serves /healthz before it starts Runnables, so a
// Runnable that blocks indefinitely cannot leave the liveness probe unanswered.
// /readyz stays failing until registration completes, which happens while the
// Runnable is still executing, as it is in run().
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

	// Readiness is scoped to registration, not to the loop that follows it.
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
	// The listener is bound at manager construction but only served once Start
	// reaches it, so retry rather than racing the first request.
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
