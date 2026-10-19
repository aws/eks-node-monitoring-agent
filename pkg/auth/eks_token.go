// Package auth generates the bearer tokens the agent uses to authenticate to
// the EKS API server.
//
// EKS kubeconfigs authenticate through a client-go exec credential plugin that
// shells out to `aws eks get-token` (or `aws-iam-authenticator token`). Running
// that plugin costs a process launch every time client-go refreshes the
// credential: the aws CLI alone resident-sets roughly 50MB, which on top of the
// agent's own footprint is enough to cross the container's cgroup memory limit
// and get the pod OOM killed. Because client-go caches the credential until it
// expires, the spike recurs on the token's ~15 minute refresh cycle.
//
// This package produces the same token in-process using the same library the
// plugin does, so refreshing costs no extra process and no extra memory.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/aws-iam-authenticator/pkg/token"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// tokenRefreshBuffer is how long before expiry a cached token is considered
// stale. EKS tokens live for ~15 minutes, so refreshing a minute early keeps a
// request from being signed with a token that expires in flight.
const tokenRefreshBuffer = 1 * time.Minute

// EKSTokenGenerator mints EKS authentication tokens for a single cluster and
// caches the result until it is close to expiring.
//
// Tokens are presigned STS GetCallerIdentity URLs, so generating one requires
// AWS credentials. Those are resolved by the default AWS credential chain,
// which reaches IMDS over the pod's network namespace. The agent runs with
// hostNetwork, so IMDS returns the node's instance profile: the same identity
// the exec credential plugin obtained by chrooting onto the host.
type EKSTokenGenerator struct {
	clusterName string
	generator   token.Generator

	// mu guards the cached token. A plain Mutex is enough — tokens are reused
	// for ~14 minutes, so this is contended about once per refresh.
	mu          sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

// NewEKSTokenGenerator creates a token generator for the named cluster.
func NewEKSTokenGenerator(clusterName string) (*EKSTokenGenerator, error) {
	// the cluster name ends up in the signed request as the x-k8s-aws-id header,
	// so a blank name would yield a token the API server rejects with a
	// confusing error. Fail here instead.
	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return nil, fmt.Errorf("cluster name must not be empty")
	}

	// forwardSessionName=true preserves the caller's STS session name in the
	// generated token, which keeps the node's identity legible in CloudTrail.
	// cache=false disables the library's on-disk credential cache; this process
	// holds the token in memory (see GetToken) and writing to the container
	// filesystem buys nothing.
	gen, err := token.NewGenerator(true, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create token generator: %w", err)
	}

	return &EKSTokenGenerator{
		clusterName: clusterName,
		generator:   gen,
	}, nil
}

// GetToken returns a valid EKS authentication token, reusing the cached one
// until it is within tokenRefreshBuffer of expiring.
func (g *EKSTokenGenerator) GetToken(ctx context.Context) (string, error) {
	logger := log.FromContext(ctx).WithName("eks-token-generator")

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.valid() {
		logger.V(4).Info("using cached EKS token", "expiry", g.tokenExpiry)
		return g.cachedToken, nil
	}

	tok, err := g.generator.GetWithOptions(ctx, &token.GetTokenOptions{
		ClusterID: g.clusterName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate EKS token: %w", err)
	}

	g.cachedToken = tok.Token
	g.tokenExpiry = tok.Expiration

	logger.Info("generated new EKS token", "cluster", g.clusterName, "expiry", tok.Expiration)
	return tok.Token, nil
}

// valid reports whether the cached token can still be used. Callers must hold mu.
func (g *EKSTokenGenerator) valid() bool {
	return g.cachedToken != "" && time.Now().Add(tokenRefreshBuffer).Before(g.tokenExpiry)
}

// EKSTokenTransport authenticates outbound API server requests with a token
// from Generator. It replaces the exec credential plugin that client-go would
// otherwise run, so it must be installed on the rest.Config that has had its
// ExecProvider cleared.
type EKSTokenTransport struct {
	Base      http.RoundTripper
	Generator *EKSTokenGenerator
}

// RoundTrip implements http.RoundTripper.
func (t *EKSTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.Generator.GetToken(req.Context())
	if err != nil {
		return nil, fmt.Errorf("failed to get EKS token: %w", err)
	}

	// RoundTrippers must not mutate the request they are given: the caller may
	// still read it, and client-go retries by re-sending the same request. Clone
	// it and set the header on the copy.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+tok)

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
