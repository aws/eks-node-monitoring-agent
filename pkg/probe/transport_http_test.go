package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

func httpCheck(address string) probe.Check {
	return probe.Check{Transport: probe.TransportHTTPLoopback, Address: address, Path: "/healthz"}
}

func TestHTTPLoopbackTransport_Healthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	result := NewHTTPLoopbackTransport().Do(context.Background(), httpCheck(strings.TrimPrefix(ts.URL, "http://")))
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("outcome = %q (%s), want Healthy", result.Outcome, result.Detail)
	}
}

func TestHTTPLoopbackTransport_UnhealthyStatusWithBodyDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"ebpf map write failed"}`))
	}))
	defer ts.Close()

	result := NewHTTPLoopbackTransport().Do(context.Background(), httpCheck(strings.TrimPrefix(ts.URL, "http://")))
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q, want Unhealthy", result.Outcome)
	}
	if !strings.Contains(result.Detail, "503") || !strings.Contains(result.Detail, "ebpf map write failed") {
		t.Errorf("detail %q should contain status and body", result.Detail)
	}
}

func TestHTTPLoopbackTransport_ConnectionRefusedIsUnhealthy(t *testing.T) {
	// Reserve a port, then close the listener so the connection is refused.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := strings.TrimPrefix(ts.URL, "http://")
	ts.Close()

	result := NewHTTPLoopbackTransport().Do(context.Background(), httpCheck(addr))
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q (%s), want Unhealthy for refused connection", result.Outcome, result.Detail)
	}
}

func TestHTTPLoopbackTransport_CanceledContextIsUnknown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewHTTPLoopbackTransport().Do(ctx, httpCheck(strings.TrimPrefix(ts.URL, "http://")))
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("outcome = %q, want Unknown for canceled context", result.Outcome)
	}
}
