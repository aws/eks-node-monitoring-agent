// Package corefile implements health detection for eks-networking-config-agent,
// the on-AMI agent that applies the customer-supplied CoreDNS Corefile on EKS
// Auto Mode nodes.
//
// It is NOT a standalone monitor. The networking monitor constructs a Detector
// and drives its HandleState on a fixed interval, so all Corefile conditions
// surface under the networking monitor's NetworkingReady node condition.
// Corefile detection runs on Auto Mode nodes only.
//
// The agent exposes its apply outcome as JSON at http://localhost:62350/readyz.
// HTTP 200 means the customer Corefile is live (`Applied`), or DNS is healthy
// without one (`CoreDNSCorefileConfigMapNotFound`: not opted in, or the
// ConfigMap was deleted and the last applied Corefile keeps serving). HTTP 503
// with a `reason` field indicates a failure state, or startup
// (`CoreDNSCorefileInitializing`).
//
// Severity policy: all failure reasons are Warning. Every failure traces
// back to a customer-config problem (rejected by validation, refused by CoreDNS
// on reload, reverted to the last applied Corefile, reverted to baseline). Node
// replacement re-applies the same Corefile from the ConfigMap and re-hits the
// same failure, so raising any of these to Fatal would create a replacement
// loop for a customer-config issue rather than a node-level failure. Baseline
// DNS keeps the node functional in the revert cases, so no repair signal is
// warranted. `Applied` is reported as an Info event so customers can confirm
// their Corefile took effect. An agent that reports no outcome for
// silentThreshold is a Warning for the same reason: replacement boots the same
// agent.
package corefile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/renotify"
)

const (
	// ReadyzURL is the agent's readiness endpoint on the node. Loopback +
	// fixed port; the agent is a systemd unit local to the node.
	ReadyzURL = "http://localhost:62350/readyz"

	// probeTimeout bounds a single HTTP GET so the periodic ticker cannot
	// stall on a hung endpoint.
	probeTimeout = 3 * time.Second

	// reNotifyInterval keeps an unchanged failure's Event from aging out
	// under the apiserver's default 1h event TTL.
	reNotifyInterval = 30 * time.Minute

	// silentThreshold outlasts boot and the agent's own startup retries (up to
	// 15 minutes resolving the NodeClass's configMapRef), so a longer silence
	// means the agent is not running or stuck.
	silentThreshold = 20 * time.Minute

	appliedReason      = "Applied"
	initializingReason = "CoreDNSCorefileInitializing"
)

// readyzResponse is the JSON body of GET /readyz. Fields match the agent's
// status.Status struct; unknown fields are ignored by encoding/json.
// AppliedSHA is the Corefile serving DNS; FailedSHA is one the agent rejected
// or rolled back. Reverts set both.
type readyzResponse struct {
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	AppliedSHA string `json:"appliedSha,omitempty"`
	FailedSHA  string `json:"failedSha,omitempty"`
	NodeName   string `json:"nodeName,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

func (r readyzResponse) key() notifyKey {
	return notifyKey{r.Reason, r.AppliedSHA, r.FailedSHA}
}

// shaSuffix labels each SHA so the reader can tell a rejected Corefile from
// the one serving DNS. SHAs too short to truncate are omitted.
func (r readyzResponse) shaSuffix() string {
	const truncate = 12
	var parts []string
	if len(r.AppliedSHA) >= truncate {
		parts = append(parts, "appliedSha="+r.AppliedSHA[:truncate])
	}
	if len(r.FailedSHA) >= truncate {
		parts = append(parts, "failedSha="+r.FailedSHA[:truncate])
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// Detector polls the agent's /readyz and maps its outcome onto the
// NetworkingReady condition. Create one with New and drive it from the
// networking monitor's periodic-ticker slice.
type Detector struct {
	manager monitor.Manager
	log     logr.Logger
	client  *http.Client
	url     string
	now     func() time.Time

	// A failure stays unchanged until the customer edits the ConfigMap, and
	// every NMA Warning on a node shares one client-go spam-filter bucket, so
	// notify only on change or after reNotifyInterval.
	notified *renotify.Tracker[notifyKey]

	// lastSettled is the last 200 or known failure readyz reported, so an
	// agent restart that settles on the same Corefile is not announced again.
	// It starts empty, so a new NMA process announces the current Corefile.
	lastSettled notifyKey

	// silentSince is when the current run of ticks without an outcome began;
	// zero when the last tick had one.
	silentSince time.Time

	// reportedSilent is set once CoreDNSCorefileAgentUnavailable is posted, so
	// the first outcome after it is announced even if it matches the one
	// before the silence.
	reportedSilent bool
}

type notifyKey struct {
	reason, appliedSHA, failedSHA string
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
		url:      ReadyzURL,
		now:      time.Now,
		notified: renotify.New[notifyKey](reNotifyInterval),
	}
}

// reasonMap translates the agent's failure reasons into NMA reasons.
// `Applied` and `CoreDNSCorefileConfigMapNotFound` return HTTP 200 and never
// reach this map; `CoreDNSCorefileInitializing` (startup, HTTP 503) is
// handled as no outcome yet.
var reasonMap = map[string]reasons.ReasonMeta{
	"CoreDNSCorefileValidationFailed":      reasons.CoreDNSCorefileValidationFailed,
	"CoreDNSCorefileReloadFailed":          reasons.CoreDNSCorefileReloadFailed,
	"CoreDNSCorefileRevertedToLastApplied": reasons.CoreDNSCorefileRevertedToLastApplied,
	"CoreDNSCorefileRevertedToBaseline":    reasons.CoreDNSCorefileRevertedToBaseline,
}

// HandleState scrapes /readyz once. It emits the mapped Warning on non-2xx
// with a known reason, an Info when a 200 reports a newly applied Corefile,
// and a Warning when the agent has reported no outcome for silentThreshold.
// A shorter silence (systemd restart, startup) is skipped so it does not flap
// the condition.
func (d *Detector) HandleState() error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	status, code, silent, err := d.scrape(ctx)
	if err != nil {
		return err
	}
	if silent != "" {
		return d.reportSilent(ctx, silent)
	}
	d.silentSince = time.Time{}
	if d.reportedSilent {
		d.reportedSilent = false
		d.notified.Reset()
		d.lastSettled = notifyKey{}
	}

	// 200 covers Applied and CoreDNSCorefileConfigMapNotFound, both healthy
	// per the agent's ReadyzHandler.
	if code == http.StatusOK {
		d.notified.Reset()
		return d.reportApplied(ctx, status)
	}

	meta, known := reasonMap[status.Reason]
	if !known {
		// A reason string this build predates. Log at V(1) so it surfaces in
		// journald without leaking a condition.
		d.log.V(1).Info("readyz reason not mapped; skipping",
			"reason", status.Reason, "detail", status.Detail)
		return nil
	}

	key := status.key()
	d.lastSettled = key
	now := d.now()
	if !d.notified.ShouldNotify(key, now) {
		return nil
	}

	msg := status.Detail
	if msg == "" {
		msg = status.Reason
	}
	msg += status.shaSuffix()
	if err := d.manager.Notify(ctx, meta.Builder().Message(msg).Build()); err != nil {
		return err
	}
	d.notified.Notified(key, now)
	return nil
}

// scrape reads /readyz. silent is non-empty, and says why, when the agent
// gave no outcome: it did not answer, sent an unreadable body, or is still
// initializing.
func (d *Detector) scrape(ctx context.Context) (status readyzResponse, code int, silent string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return status, 0, "", fmt.Errorf("build readyz request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		d.log.V(1).Info("readyz probe failed; skipping tick", "err", err.Error())
		return status, 0, "readyz did not answer: " + err.Error(), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		d.log.V(1).Info("readyz body read failed; skipping tick", "err", err.Error())
		return status, resp.StatusCode, "readyz body read failed: " + err.Error(), nil
	}
	if err := json.Unmarshal(body, &status); err != nil {
		d.log.V(1).Info("readyz JSON parse failed; skipping tick", "err", err.Error(), "body", string(body))
		return status, resp.StatusCode, "readyz body is not valid JSON", nil
	}
	if status.Reason == initializingReason {
		return status, resp.StatusCode, "the agent is still initializing: " + status.Detail, nil
	}
	return status, resp.StatusCode, "", nil
}

// reportSilent raises CoreDNSCorefileAgentUnavailable once the agent has gone
// silentThreshold without an outcome, and again every reNotifyInterval while
// it stays silent.
func (d *Detector) reportSilent(ctx context.Context, cause string) error {
	now := d.now()
	if d.silentSince.IsZero() {
		d.silentSince = now
	}
	silentFor := now.Sub(d.silentSince)
	if silentFor < silentThreshold {
		return nil
	}

	meta := reasons.CoreDNSCorefileAgentUnavailable
	key := notifyKey{reason: meta.Template()}
	if !d.notified.ShouldNotify(key, now) {
		return nil
	}
	msg := fmt.Sprintf("eks-networking-config-agent has reported no Corefile outcome for at least %d minutes, so a custom Corefile set on the NodeClass may not be applied; last check: %s",
		int(silentFor.Minutes()), cause)
	if err := d.manager.Notify(ctx, meta.Builder().Message(msg).Build()); err != nil {
		return err
	}
	d.notified.Notified(key, now)
	d.reportedSilent = true
	return nil
}

// reportApplied emits an Info when readyz reports Applied for a Corefile that
// differs from the last settled outcome. The agent re-reports an unchanged
// outcome on every poll, which stays quiet.
func (d *Detector) reportApplied(ctx context.Context, status readyzResponse) error {
	key := status.key()
	if key == d.lastSettled {
		return nil
	}
	if status.Reason == appliedReason && status.AppliedSHA != "" {
		msg := "CoreDNS is serving the custom Corefile" + status.shaSuffix()
		if err := d.manager.Notify(ctx, reasons.CoreDNSCorefileApplied.Builder().Message(msg).Build()); err != nil {
			return err
		}
	}
	d.lastSettled = key
	return nil
}
