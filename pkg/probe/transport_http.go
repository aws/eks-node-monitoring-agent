package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/probe"
)

const (
	// httpCheckTimeout bounds a single HTTP check execution.
	httpCheckTimeout = 5 * time.Second
	// maxDetailBytes bounds how much of a response body is captured into the
	// result detail for condition messages.
	maxDetailBytes = 1024
)

// HTTPLoopbackTransport executes checks as HTTP GET requests against
// local-only addresses. A 2xx response is healthy. Any non-2xx response or a
// failure to connect to the agent's endpoint is unhealthy — an agent that is
// not answering its own health surface is not healthy.
type HTTPLoopbackTransport struct {
	client *http.Client
}

// NewHTTPLoopbackTransport returns an HTTP transport with a bounded
// per-check timeout.
func NewHTTPLoopbackTransport() *HTTPLoopbackTransport {
	return &HTTPLoopbackTransport{
		client: &http.Client{Timeout: httpCheckTimeout},
	}
}

func (t *HTTPLoopbackTransport) Do(ctx context.Context, check probe.Check) Result {
	url := "http://" + check.Address + check.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// A malformed spec, not an agent failure.
		return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("building request for %s: %v", url, err)}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The agent is shutting down or the caller gave up; do not
			// attribute this to the probed agent.
			return Result{Outcome: OutcomeUnknown, Detail: fmt.Sprintf("check canceled: %v", ctx.Err())}
		}
		return Result{Outcome: OutcomeUnhealthy, Detail: fmt.Sprintf("GET %s: %v", url, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{Outcome: OutcomeHealthy}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDetailBytes))
	detail := fmt.Sprintf("GET %s: %s", url, resp.Status)
	if b := strings.TrimSpace(string(body)); b != "" {
		detail += ": " + b
	}
	return Result{Outcome: OutcomeUnhealthy, Detail: detail}
}
