package config

import (
	"cmp"
	"errors"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Config is the telemetry block of the NMA config file.
type Config struct {
	Enabled        bool                 `json:"enabled,omitempty"`
	Report         ReportSettings       `json:"report,omitempty"`
	NotReporting   NotReportingSettings `json:"notReporting,omitempty"`
	TargetDefaults TargetSettings       `json:"targetDefaults,omitempty"`
	Targets        []Target             `json:"targets,omitempty"`
}

// ReportSettings configures the reports sent to the telemetry API.
type ReportSettings struct {
	// Interval is how often a report is sent.
	Interval metav1.Duration `json:"interval,omitempty"`
}

// NotReportingSettings configures when a target is reported as not reporting.
type NotReportingSettings struct {
	// AfterFailedIntervals is how many failed scrapes in a row it takes for a
	// target to be sent as eks_not_reporting.
	AfterFailedIntervals int `json:"afterFailedIntervals,omitempty"`
}

// TargetSettings are the settings a target can override from targetDefaults.
type TargetSettings struct {
	// Path is the HTTP path scraped on the target's endpoint.
	Path string `json:"path,omitempty"`
	// Timeout bounds each scrape.
	Timeout metav1.Duration `json:"timeout,omitempty"`
	// MaxBytes is the largest scrape response read; a larger one fails the scrape.
	MaxBytes *resource.Quantity `json:"maxBytes,omitempty"`
	// Expected defines when a failed scrape counts toward not reporting.
	Expected ExpectedPolicy `json:"expected,omitempty"`
	// MaxSeries is the default for each metric's MaxSeries.
	MaxSeries int `json:"maxSeries,omitempty"`
}

// ExpectedPolicy is when a target is expected to be reachable.
type ExpectedPolicy string

const (
	// ExpectedAlways counts every failed scrape toward not reporting.
	ExpectedAlways ExpectedPolicy = "always"
	// ExpectedOnceSeen only counts failed scrapes once the target has been
	// scraped successfully since NMA started.
	ExpectedOnceSeen ExpectedPolicy = "onceSeen"
)

// Target is a component whose metrics are sent.
type Target struct {
	// Component is the report scope name.
	Component string `json:"component"`
	// Endpoint is where the target is scraped. If nil, the target is NMA itself.
	Endpoint *TargetEndpoint `json:"endpoint,omitempty"`
	// Identity, if set, is checked on every scrape response.
	Identity *TargetIdentity `json:"identity,omitempty"`
	// Metrics is the allowlist of metrics sent from the target.
	Metrics []MetricSpec `json:"metrics,omitempty"`
	TargetSettings
}

// TargetEndpoint is the address and port a target is scraped on.
type TargetEndpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// TargetIdentity identifies the process behind a target's endpoint.
type TargetIdentity struct {
	// RequireMetric must be in every scrape response, or the scrape fails.
	RequireMetric string `json:"requireMetric"`
}

// MetricSpec is an allowlisted metric.
type MetricSpec struct {
	Name string     `json:"name"`
	Type MetricType `json:"type,omitempty"`
	Unit string     `json:"unit,omitempty"`
	// Labels are the labels kept; series that differ only in dropped labels
	// are combined into one.
	Labels []string `json:"labels,omitempty"`
	// Aggregate is how combined gauge series are merged.
	Aggregate Aggregation `json:"aggregate,omitempty"`
	// MaxSeries is the most series the metric may have after Labels are
	// applied; with more, the metric is left out of the report.
	MaxSeries int `json:"maxSeries,omitempty"`
}

// MetricType is a metric's type. Unset is a gauge; counters are sent as
// deltas.
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

// Aggregation is how gauge series combined by Labels are merged.
type Aggregation string

const (
	AggregateSum Aggregation = "sum"
	AggregateMax Aggregation = "max"
	AggregateMin Aggregation = "min"
)

// Defaults for unset fields.
const (
	DefaultReportInterval       = 5 * time.Minute
	DefaultAfterFailedIntervals = 2
	DefaultTargetPath           = "/metrics"
	DefaultTargetTimeout        = 2 * time.Second
	DefaultTargetMaxBytes       = 1 << 20
	DefaultExpectedPolicy       = ExpectedAlways
	DefaultMaxSeries            = 50
)

// Resolved is a target's settings with targetDefaults and defaults applied.
type Resolved struct {
	Path      string
	Timeout   time.Duration
	MaxBytes  int64
	Expected  ExpectedPolicy
	MaxSeries int
}

// Resolve returns t's settings, falling back to targetDefaults and then the
// defaults.
func (cfg *Config) Resolve(target Target) Resolved {
	defaults := cfg.TargetDefaults
	resolved := Resolved{
		Path:      cmp.Or(target.Path, defaults.Path, DefaultTargetPath),
		Timeout:   cmp.Or(target.Timeout.Duration, defaults.Timeout.Duration, DefaultTargetTimeout),
		MaxBytes:  DefaultTargetMaxBytes,
		Expected:  cmp.Or(target.Expected, defaults.Expected, DefaultExpectedPolicy),
		MaxSeries: cmp.Or(target.MaxSeries, defaults.MaxSeries, DefaultMaxSeries),
	}
	if maxBytes := cmp.Or(target.MaxBytes, defaults.MaxBytes); maxBytes != nil {
		resolved.MaxBytes = maxBytes.Value()
	}
	return resolved
}

// ReportInterval returns the report interval, or the default if unset.
func (cfg *Config) ReportInterval() time.Duration {
	return cmp.Or(cfg.Report.Interval.Duration, DefaultReportInterval)
}

// NotReportingThreshold returns AfterFailedIntervals, or the default if unset.
func (cfg *Config) NotReportingThreshold() int {
	return cmp.Or(cfg.NotReporting.AfterFailedIntervals, DefaultAfterFailedIntervals)
}

// IsEnabled returns true if the config is set and enabled.
func (cfg *Config) IsEnabled() bool {
	return cfg != nil && cfg.Enabled
}

// Validate checks the config. The config is optional and may be nil.
func (cfg *Config) Validate() error {
	if cfg == nil {
		return nil
	}
	if !cfg.Enabled && !reflect.DeepEqual(cfg, &Config{}) {
		return errors.New("telemetry: other fields can only be set when enabled is true")
	}
	// TODO: validate units, metric types, component names and explicit
	// zero values.
	return nil
}
