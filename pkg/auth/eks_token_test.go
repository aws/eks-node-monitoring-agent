package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewEKSTokenGenerator(t *testing.T) {
	t.Run("builds a generator for the cluster", func(t *testing.T) {
		generator, err := NewEKSTokenGenerator("test-cluster")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if generator.clusterName != "test-cluster" {
			t.Fatalf("expected cluster name %q, got %q", "test-cluster", generator.clusterName)
		}
		if generator.generator == nil {
			t.Fatal("expected an underlying token generator")
		}
	})

	t.Run("trims surrounding whitespace from the cluster name", func(t *testing.T) {
		generator, err := NewEKSTokenGenerator("  test-cluster\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if generator.clusterName != "test-cluster" {
			t.Fatalf("expected cluster name %q, got %q", "test-cluster", generator.clusterName)
		}
	})

	// a blank cluster name would produce a token the API server rejects, so it
	// has to fail at construction rather than on the first request.
	for _, clusterName := range []string{"", " ", "\t\n"} {
		t.Run(fmt.Sprintf("rejects the blank cluster name %q", clusterName), func(t *testing.T) {
			if _, err := NewEKSTokenGenerator(clusterName); err == nil {
				t.Fatalf("expected an error for cluster name %q", clusterName)
			}
		})
	}
}

func TestGeneratorValid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		token  string
		expiry time.Time
		want   bool
	}{
		{"no cached token", "", time.Now().Add(time.Hour), false},
		{"fresh token", "tok", time.Now().Add(time.Hour), true},
		{"expired token", "tok", time.Now().Add(-time.Minute), false},
		// within the refresh buffer the token is still technically valid, but a
		// request signed with it could expire in flight.
		{"token inside the refresh buffer", "tok", time.Now().Add(tokenRefreshBuffer / 2), false},
		{"token just outside the refresh buffer", "tok", time.Now().Add(tokenRefreshBuffer + time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &EKSTokenGenerator{cachedToken: tc.token, tokenExpiry: tc.expiry}
			if got := g.valid(); got != tc.want {
				t.Fatalf("expected valid()==%v, got %v", tc.want, got)
			}
		})
	}
}

func TestGetTokenReturnsCachedToken(t *testing.T) {
	// generator is deliberately nil: if GetToken tried to mint a new token
	// instead of serving the cache, this would panic.
	g := &EKSTokenGenerator{
		clusterName: "test-cluster",
		cachedToken: "cached-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}

	got, err := g.GetToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cached-token" {
		t.Fatalf("expected the cached token, got %q", got)
	}
}

func TestEKSTokenTransport(t *testing.T) {
	newGenerator := func(tok string) *EKSTokenGenerator {
		return &EKSTokenGenerator{
			clusterName: "test-cluster",
			cachedToken: tok,
			tokenExpiry: time.Now().Add(time.Hour),
		}
	}

	t.Run("authorizes the request with a bearer token", func(t *testing.T) {
		var seen string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("Authorization")
		}))
		defer server.Close()

		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}

		transport := &EKSTokenTransport{Base: server.Client().Transport, Generator: newGenerator("tok")}
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		if want := "Bearer tok"; seen != want {
			t.Fatalf("expected header %q, got %q", want, seen)
		}
		// RoundTrippers must leave the request they are handed alone, because
		// client-go may re-send it on retry.
		if got := req.Header.Get("Authorization"); got != "" {
			t.Fatalf("the original request was mutated: %q", got)
		}
	})

	t.Run("propagates errors from the base transport", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
		if err != nil {
			t.Fatal(err)
		}

		wantErr := errors.New("boom")
		transport := &EKSTokenTransport{
			Base:      roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, wantErr }),
			Generator: newGenerator("tok"),
		}
		if _, err := transport.RoundTrip(req); !errors.Is(err, wantErr) {
			t.Fatalf("expected the base transport error, got %v", err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
