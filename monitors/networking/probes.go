package networking

import (
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiprobe "github.com/aws/eks-node-monitoring-agent/api/probe"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

// probeSpecs returns the subagent health probes supervised by the networking
// monitor for the current delivery path. Probes replace only agent-mediated
// liveness detection; checkpoint cross-checks and configuration validation
// remain handler-based.
func (m *NetworkingMonitor) probeSpecs() []apiprobe.Spec {
	if !slices.Contains(m.runtimeContext.Tags(), config.EKSAuto) {
		// On the DaemonSet path IPAMD runs inside the VPC CNI pod and is
		// observed via the existing process scan in handleIPAMD; its gRPC
		// health surface is a future transport.
		return nil
	}
	return []apiprobe.Spec{
		{
			Subsystem: "ipamd",
			Checks: apiprobe.Checks{
				Liveness: apiprobe.Check{
					Transport: apiprobe.TransportSystemdDBus,
					Address:   "ipamd.service",
				},
			},
			ReasonOnFail:     "IPAMDNotRunning",
			Interval:         metav1.Duration{Duration: 30 * time.Second},
			FailureThreshold: 3,
			// Tolerate ipamd.service coming up after the monitoring agent
			// during boot; systemd ordering does not sequence the two.
			StartupGracePeriod: metav1.Duration{Duration: 5 * time.Minute},
		},
	}
}
