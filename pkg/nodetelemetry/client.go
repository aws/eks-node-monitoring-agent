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

const metricsAPIPath = "/apis/telemetry.eks.amazonaws.com/v1/metrics"

type Outcome int

const (
	Accepted Outcome = iota
	Dropped
	Hold
	// TODO: add Skipped, for reports too large to send.
)

func (o Outcome) String() string {
	return [...]string{"accepted", "dropped", "hold"}[o]
}

type Result struct {
	Outcome Outcome
	Status  int
	Message string
}

type Client struct {
	http *http.Client
	url  string
}

// defaultSendTimeout bounds a report's POST when the rest config doesn't set a
// Timeout, so a stalled request can't block the reporting loop. It's longer
// than kube-apiserver's default request timeout (60s), so a stuck request
// normally ends with the API server's 504 rather than this client giving up.
const defaultSendTimeout = 75 * time.Second

func NewClient(cfg *rest.Config) (*Client, error) {
	cfg = rest.CopyConfig(cfg)
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultSendTimeout
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("nodetelemetry: build http client: %w", err)
	}
	return NewClientFromURL(hc, strings.TrimSuffix(cfg.Host, "/")+metricsAPIPath), nil
}

func NewClientFromURL(hc *http.Client, url string) *Client {
	return &Client{http: hc, url: url}
}

func (c *Client) Send(ctx context.Context, r *Report) (Result, error) {
	if r == nil {
		return Result{}, errors.New("nodetelemetry: nil report")
	}
	body, err := r.Marshal()
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Outcome: Hold, Message: err.Error()}, nil
	}
	defer resp.Body.Close()
	// TODO: decode partial-success counts and google.rpc.Status bodies.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	res := Result{Status: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	switch {
	case resp.StatusCode < 300:
		res.Outcome = Accepted
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		res.Outcome = Hold
	default:
		res.Outcome = Dropped
	}
	return res, nil
}
