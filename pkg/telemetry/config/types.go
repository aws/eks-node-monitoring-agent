package config

import (
	"cmp"
	"errors"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Config struct {
	Enabled        bool                 `json:"enabled,omitempty"`
	Report         ReportSettings       `json:"report,omitempty"`
	NotReporting   NotReportingSettings `json:"notReporting,omitempty"`
	TargetDefaults TargetSettings       `json:"targetDefaults,omitempty"`
	Targets        []Target             `json:"targets,omitempty"`
}

type ReportSettings struct {
	Interval metav1.Duration `json:"interval,omitempty"`
}

type NotReportingSettings struct {
	AfterFailedIntervals int `json:"afterFailedIntervals,omitempty"`
}

type TargetSettings struct {
	Path      string             `json:"path,omitempty"`
	Timeout   metav1.Duration    `json:"timeout,omitempty"`
	MaxBytes  *resource.Quantity `json:"maxBytes,omitempty"`
	Expected  ExpectedPolicy     `json:"expected,omitempty"`
	MaxSeries int                `json:"maxSeries,omitempty"`
}

type ExpectedPolicy string

const (
	ExpectedAlways   ExpectedPolicy = "always"
	ExpectedOnceSeen ExpectedPolicy = "onceSeen"
)

type Target struct {
	Component string          `json:"component"`
	Endpoint  *TargetEndpoint `json:"endpoint,omitempty"`
	Identity  *TargetIdentity `json:"identity,omitempty"`
	Metrics   []MetricSpec    `json:"metrics,omitempty"`
	TargetSettings
}

type TargetEndpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type TargetIdentity struct {
	RequireMetric string `json:"requireMetric"`
}

type MetricSpec struct {
	Name      string     `json:"name"`
	Type      MetricType `json:"type,omitempty"`
	Unit      string     `json:"unit,omitempty"`
	Labels    []string   `json:"labels,omitempty"`
	Aggregate string     `json:"aggregate,omitempty"`
	MaxSeries int        `json:"maxSeries,omitempty"`
}

type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

const (
	DefaultReportInterval       = 5 * time.Minute
	DefaultAfterFailedIntervals = 2
	DefaultTargetPath           = "/metrics"
	DefaultTargetTimeout        = 2 * time.Second
	DefaultTargetMaxBytes       = 1 << 20
	DefaultExpectedPolicy       = ExpectedAlways
	DefaultMaxSeries            = 50
)

type Resolved struct {
	Path      string
	Timeout   time.Duration
	MaxBytes  int64
	Expected  ExpectedPolicy
	MaxSeries int
}

func (c *Config) Resolve(t Target) Resolved {
	d := c.TargetDefaults
	r := Resolved{
		Path:      cmp.Or(t.Path, d.Path, DefaultTargetPath),
		Timeout:   cmp.Or(t.Timeout.Duration, d.Timeout.Duration, DefaultTargetTimeout),
		MaxBytes:  DefaultTargetMaxBytes,
		Expected:  cmp.Or(t.Expected, d.Expected, DefaultExpectedPolicy),
		MaxSeries: cmp.Or(t.MaxSeries, d.MaxSeries, DefaultMaxSeries),
	}
	if q := cmp.Or(t.MaxBytes, d.MaxBytes); q != nil {
		r.MaxBytes = q.Value()
	}
	return r
}

func (c *Config) ReportInterval() time.Duration {
	return cmp.Or(c.Report.Interval.Duration, DefaultReportInterval)
}

func (c *Config) NotReportingThreshold() int {
	return cmp.Or(c.NotReporting.AfterFailedIntervals, DefaultAfterFailedIntervals)
}

func (c *Config) IsEnabled() bool {
	return c != nil && c.Enabled
}

func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if !c.Enabled && !reflect.DeepEqual(c, &Config{}) {
		return errors.New("telemetry: other fields can only be set when enabled is true")
	}
	// TODO: validate units, metric types, component names and explicit
	// zero values.
	return nil
}
