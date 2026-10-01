package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestHTTPLoopbackTransport_MarksTruncatedBody(t *testing.T) {
	for _, tc := range []struct {
		name          string
		bodyLen       int
		wantTruncated bool
	}{
		{"body at the limit", maxDetailBytes, false},
		{"body past the limit", maxDetailBytes + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(strings.Repeat("x", tc.bodyLen)))
			}))
			defer ts.Close()

			result := NewHTTPLoopbackTransport().Do(context.Background(), httpCheck(strings.TrimPrefix(ts.URL, "http://")))
			kept := strings.Repeat("x", maxDetailBytes)
			if !strings.Contains(result.Detail, kept) || strings.Contains(result.Detail, kept+"x") {
				t.Errorf("detail should keep exactly %d body bytes, got %q", maxDetailBytes, result.Detail)
			}
			if got := strings.HasSuffix(result.Detail, " (truncated)"); got != tc.wantTruncated {
				t.Errorf("truncation marker = %v, want %v: %q", got, tc.wantTruncated, result.Detail)
			}
		})
	}
}

func TestHTTPLoopbackTransport_RedirectIsUnhealthyAndNotFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/healthz", http.StatusFound)
	}))
	defer ts.Close()

	result := NewHTTPLoopbackTransport().Do(context.Background(), httpCheck(strings.TrimPrefix(ts.URL, "http://")))
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q (%s), want Unhealthy for a redirect", result.Outcome, result.Detail)
	}
	if !strings.Contains(result.Detail, "302") {
		t.Errorf("detail %q should show the redirect status", result.Detail)
	}
	if followed.Load() {
		t.Error("the transport followed the redirect")
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
