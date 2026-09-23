// Package corefile implements health detection for eks-networking-config-agent,
// the on-AMI agent that applies the customer-supplied CoreDNS Corefile on EKS
// Auto Mode nodes.
//
// It is NOT a standalone monitor. The networking monitor constructs a Detector
// and drives its HandleState on a fixed interval, so all Corefile conditions
// surface under the networking monitor's NetworkingReady node condition.
// Corefile detection runs on Auto Mode nodes only.
//
// The agent exposes its apply outcome as JSON at http://localhost:8082/readyz.
// HTTP 200 means the customer Corefile is live (`Applied`) or the customer
// has not opted in (`CoreDNSCorefileConfigMapNotFound`, baseline serving);
// HTTP 503 with a `reason` field indicates a specific failure state.
//
// Severity policy: all five reasons are Warning. Every outcome traces back
// to a customer-config problem (bad Corefile, unloadable, crash-looping on
// customer cfg, agent reverted). Node replacement re-applies the same
// Corefile from the ConfigMap and re-hits the same failure, so raising any
// of these to Fatal would create a replacement loop for a customer-config
// issue rather than a node-level failure. Baseline DNS keeps the node
// functional in the revert cases, so no repair signal is warranted.
package corefile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

const (
	// ReadyzURL is the agent's readiness endpoint on the node. Loopback +
	// fixed port; the agent is a systemd unit local to the node.
	ReadyzURL = "http://localhost:8082/readyz"

	// probeTimeout bounds a single HTTP GET so the periodic ticker cannot
	// stall on a hung endpoint.
	probeTimeout = 3 * time.Second
)

// readyzResponse is the JSON body of GET /readyz. Fields match the agent's
// status.Status struct; unknown fields are ignored by encoding/json.
// AppliedSHA is the Corefile serving DNS; FailedSHA is one the agent rejected
// or rolled back. Never both: which one is set depends on the reason.
type readyzResponse struct {
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	AppliedSHA string `json:"appliedSha,omitempty"`
	FailedSHA  string `json:"failedSha,omitempty"`
	NodeName   string `json:"nodeName,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// shaSuffix labels the SHA so the reader can tell a rejected Corefile from the
// one serving DNS. Returns "" when neither is present or is too short to
// truncate.
func (r readyzResponse) shaSuffix() string {
	const truncate = 12
	if len(r.FailedSHA) >= truncate {
		return fmt.Sprintf(" (failedSha=%s)", r.FailedSHA[:truncate])
	}
	if len(r.AppliedSHA) >= truncate {
		return fmt.Sprintf(" (appliedSha=%s)", r.AppliedSHA[:truncate])
	}
	return ""
}

// Detector polls the agent's /readyz and maps its outcome onto the
// NetworkingReady condition. Create one with New and drive it from the
// networking monitor's periodic-ticker slice.
type Detector struct {
	manager monitor.Manager
	log     logr.Logger
	client  *http.Client
	url     string
}

// New returns a Detector wired to the networking monitor's manager so
// conditions surface under NetworkingReady. Emits through the shared manager
// only; the Detector itself has no goroutines.
func New(manager monitor.Manager, log logr.Logger) *Detector {
	return &Detector{
		manager: manager,
		log:     log,
		// Bypass HTTP_PROXY/HTTPS_PROXY so a proxied environment does not
		// misroute the loopback scrape.
		client: &http.Client{
			Timeout:   probeTimeout,
			Transport: &http.Transport{Proxy: nil},
		},
		url: ReadyzURL,
	}
}

// reasonMap translates the agent's status.Reason strings into NMA reasons.
// `Applied` and `CoreDNSCorefileConfigMapNotFound` return HTTP 200 and never
// reach this map; `CoreDNSCorefileInitializing` (startup, HTTP 503 briefly)
// is intentionally omitted so a Detector poll during agent startup does not
// emit a spurious condition.
var reasonMap = map[string]reasons.ReasonMeta{
	"CoreDNSCorefileValidationFailed":      reasons.CoreDNSCorefileValidationFailed,
	"CoreDNSCorefileReloadFailed":          reasons.CoreDNSCorefileReloadFailed,
	"CoreDNSCorefileRevertedToLastApplied": reasons.CoreDNSCorefileRevertedToLastApplied,
	"CoreDNSCorefileRevertedToBaseline":    reasons.CoreDNSCorefileRevertedToBaseline,
}

// HandleState scrapes /readyz once and emits the mapped condition on
// non-2xx with a known reason. Transport errors and unknown reasons are
// logged and skipped so a temporarily unreachable agent (systemd restart,
// pre-Applied startup window) does not flap the condition.
func (d *Detector) HandleState() error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return fmt.Errorf("build readyz request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		// Agent not up (yet), systemd restart, or transient loopback blip.
		// Do not emit: Detector will observe again on the next tick.
		d.log.V(1).Info("readyz probe failed; skipping tick", "err", err.Error())
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		d.log.V(1).Info("readyz body read failed; skipping tick", "err", err.Error())
		return nil
	}

	// 200 covers Applied and CoreDNSCorefileConfigMapNotFound (baseline
	// serving is a healthy state per the agent's ReadyzHandler).
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var body503 readyzResponse
	if err := json.Unmarshal(body, &body503); err != nil {
		d.log.V(1).Info("readyz JSON parse failed; skipping tick", "err", err.Error(), "body", string(body))
		return nil
	}

	meta, known := reasonMap[body503.Reason]
	if !known {
		// Startup (`CoreDNSCorefileInitializing`) or a reason string this
		// build predates. Log at V(1) so an unexpected reason surfaces in
		// journald without leaking a condition.
		d.log.V(1).Info("readyz reason not mapped; skipping",
			"reason", body503.Reason, "detail", body503.Detail)
		return nil
	}

	msg := body503.Detail
	if msg == "" {
		msg = body503.Reason
	}
	msg += body503.shaSuffix()
	return d.manager.Notify(ctx, meta.Builder().Message(msg).Build())
}
