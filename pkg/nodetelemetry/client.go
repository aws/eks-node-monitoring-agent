package nodetelemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

const (
	metricsAPIPath      = "/apis/telemetry.eks.amazonaws.com/v1/metrics"
	protobufContentType = "application/x-protobuf"
	gzipContentEncoding = "gzip"
	// maxMessageBytes is how much of a response body is kept in Result.Message.
	maxMessageBytes = 512
)

// Outcome is how a send's response is handled.
type Outcome string

const (
	// Accepted is a 2xx: counter baselines advance.
	Accepted Outcome = "accepted"
	// Dropped is a non-429 4xx: the report should not be accepted if resent, so
	// counter baselines should advance.
	Dropped Outcome = "dropped"
	// Hold is a 429, 5xx or transport error: counter baselines should not change
	// so the next report covers the gap.
	Hold Outcome = "hold"
	// TODO: add Skipped, for reports too large to send.
)

// Result is the result of sending a report.
type Result struct {
	Outcome Outcome
	// Status is the HTTP status code, or 0 if no response was received.
	Status int
	// Message is the first maxMessageBytes of the response body, or the transport
	// error.
	Message string
}

// Client sends reports to the telemetry API.
type Client struct {
	http *http.Client
	url  string
}

// defaultSendTimeout bounds a report's POST when the rest config doesn't set a
// Timeout, so a stalled request can't block the reporting loop. It's longer
// than kube-apiserver's default request timeout (60s), so a stuck request
// normally ends with the API server's 504 rather than this client giving up.
const defaultSendTimeout = 75 * time.Second

// NewClient returns a Client that sends to the telemetry API on cfg's host
// with cfg's credentials.
func NewClient(cfg *rest.Config) (*Client, error) {
	cfg = rest.CopyConfig(cfg)
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultSendTimeout
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("nodetelemetry: build http client: %w", err)
	}
	return NewClientFromURL(httpClient, strings.TrimSuffix(cfg.Host, "/")+metricsAPIPath), nil
}

// NewClientFromURL returns a Client that sends to url using hc.
func NewClientFromURL(httpClient *http.Client, url string) *Client {
	return &Client{http: httpClient, url: url}
}

// Send posts the report to the API server.
func (client *Client) Send(ctx context.Context, report *Report) (Result, error) {
	if report == nil {
		return Result{}, errors.New("nodetelemetry: nil report")
	}
	body, err := report.Marshal()
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", protobufContentType)
	req.Header.Set("Content-Encoding", gzipContentEncoding)
	resp, err := client.http.Do(req)
	if err != nil {
		return Result{Outcome: Hold, Message: err.Error()}, nil
	}
	defer resp.Body.Close()
	// TODO: decode partial-success counts and google.rpc.Status bodies.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes))

	res := Result{Status: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	switch {
	case resp.StatusCode < http.StatusMultipleChoices:
		res.Outcome = Accepted
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		res.Outcome = Hold
	default:
		res.Outcome = Dropped
	}
	return res, nil
}
