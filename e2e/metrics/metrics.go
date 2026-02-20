package metrics

import (
	"flag"
)

const MetricNamespace = "EKSNodeMonitoringAgent"

var MetricsEnabled bool

func init() {
	flag.BoolVar(&MetricsEnabled, "enable-metrics", false, "Whether to collect usage and timing metrics from the agent")
}
