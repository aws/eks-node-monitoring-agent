package nodetelemetry

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"regexp"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
)

// Resource attributes set on every report.
const (
	AttrNodeName       = "k8s.node.name"
	AttrHostID         = "host.id"
	AttrHostArch       = "host.arch"
	AttrHostType       = "host.type"
	AttrZoneID         = "aws.zone_id"
	AttrKubeletVersion = "k8s.kubelet.version"
	AttrComputeType    = "aws.eks.compute_type"
)

// Reserved metrics and their attributes.
const (
	MetricEksProblem       = "eks_problem"
	MetricEksNotReporting  = "eks_not_reporting"
	ProblemAttrType        = "problem.type"
	ProblemAttrReason      = "problem.reason"
	ProblemAttrResource    = "resource."
	NotReportingAttrReason = "reason"
)

// MaxPointsPerMetric should be kept as the most points the telemetry extension
// accepts for one metric. Points beyond this are expected to be dropped by the
// extension. Collectors that cap their own metrics should cap at or below it.
const MaxPointsPerMetric = 50

// unitDimensionless is the OTLP unit for a value without one.
const unitDimensionless = "1"

var attrValueRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,64}$`)

// NodeAttrs are the node's resource attributes. Empty values are omitted.
type NodeAttrs struct {
	NodeName       string
	HostID         string
	HostArch       string
	HostType       string
	ZoneID         string
	KubeletVersion string
	ComputeType    string
}

// Attrs are a data point's attributes.
type Attrs map[string]string

// GaugePoint is a gauge's value at the end of the report.
type GaugePoint struct {
	Value float64
	Attrs Attrs
}

// SumPoint is a counter's increase since StartTime. Points with a zero Delta
// are left out.
type SumPoint struct {
	Delta float64
	Attrs Attrs
	// StartTime is when the increase started. If zero, the report's start
	// time is used.
	StartTime time.Time
}

// Metric is a metric's data points. Only one of Gauges or Sums may be set.
type Metric struct {
	Name   string
	Unit   string
	Gauges []GaugePoint
	Sums   []SumPoint
}

// Problem is an active problem, sent as an eks_problem point.
type Problem struct {
	Type   string
	Reason string
	// Resources identify what the problem is about, sent as "resource."
	// attributes, e.g. {"instanceId": "i-0123"}.
	Resources map[string]string
}

// Scope is one component's metrics and problems.
type Scope struct {
	Name     string
	Metrics  []Metric
	Problems []Problem
	// NotReporting is the reason the component couldn't be collected. If set,
	// the scope only carries eks_not_reporting.
	NotReporting string
}

// Report is one OTLP metrics report for the node.
type Report struct {
	metrics         pmetric.Metrics
	resourceMetrics pmetric.ResourceMetrics
	start, end      pcommon.Timestamp
}

// NewReport returns an empty report for the interval from start to end.
func NewReport(node NodeAttrs, start, end time.Time) *Report {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	attrs := resourceMetrics.Resource().Attributes()
	for key, value := range map[string]string{
		AttrNodeName:       node.NodeName,
		AttrHostID:         node.HostID,
		AttrHostArch:       node.HostArch,
		AttrHostType:       node.HostType,
		AttrZoneID:         node.ZoneID,
		AttrKubeletVersion: node.KubeletVersion,
		AttrComputeType:    node.ComputeType,
	} {
		if value != "" {
			attrs.PutStr(key, value)
		}
	}
	return &Report{
		metrics:         metrics,
		resourceMetrics: resourceMetrics,
		start:           pcommon.NewTimestampFromTime(start),
		end:             pcommon.NewTimestampFromTime(end),
	}
}

// AddScope adds the scope to the report, or nothing if it's empty. Points with
// invalid attribute values, or beyond MaxPointsPerMetric, are dropped.
func (report *Report) AddScope(scope Scope) {
	if len(scope.Metrics) == 0 && len(scope.Problems) == 0 && scope.NotReporting == "" {
		return
	}
	scopeMetrics := report.resourceMetrics.ScopeMetrics().AppendEmpty()
	scopeMetrics.Scope().SetName(scope.Name)
	if scope.NotReporting != "" {
		point := gauge(scopeMetrics, MetricEksNotReporting, unitDimensionless).AppendEmpty()
		point.SetIntValue(1)
		point.SetTimestamp(report.end)
		point.Attributes().PutStr(NotReportingAttrReason, scope.NotReporting)
		return
	}
	for _, metric := range scope.Metrics {
		switch {
		case metric.Sums != nil && metric.Gauges != nil:
			// each Metric becomes one OTLP metric, which can only have one data type so a
			// Metric must set either Gauges or Sums, not both
			// https://github.com/open-telemetry/opentelemetry-proto/blob/09769935deb8b2ebcf73ad1e2142b691d17bf70b/opentelemetry/proto/metrics/v1/metrics.proto#L201-L210
			// TODO: count toward dropped metrics.
		case metric.Sums != nil:
			report.addSum(scopeMetrics, metric)
		case metric.Gauges != nil:
			report.addGauge(scopeMetrics, metric)
		}
	}
	if len(scope.Problems) > 0 {
		report.scopeMetrics(scopeMetrics, scope.Problems)
	}
}

func (report *Report) addGauge(scopeMetrics pmetric.ScopeMetrics, metric Metric) {
	points := gauge(scopeMetrics, metric.Name, metric.Unit)
	for _, gaugePoint := range metric.Gauges {
		if points.Len() == MaxPointsPerMetric || !validAttrs(gaugePoint.Attrs) {
			// TODO: count dropped points.
			continue
		}
		point := points.AppendEmpty()
		point.SetDoubleValue(gaugePoint.Value)
		point.SetTimestamp(report.end)
		putAttrs(point.Attributes(), gaugePoint.Attrs)
	}
}

func (report *Report) addSum(scopeMetrics pmetric.ScopeMetrics, metric Metric) {
	sum := newMetric(scopeMetrics, metric.Name, metric.Unit).SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(true)
	points := sum.DataPoints()
	for _, sumPoint := range metric.Sums {
		if sumPoint.Delta == 0 || points.Len() == MaxPointsPerMetric || !validAttrs(sumPoint.Attrs) {
			// TODO: count dropped points.
			continue
		}
		point := points.AppendEmpty()
		point.SetDoubleValue(sumPoint.Delta)
		point.SetStartTimestamp(report.start)
		if !sumPoint.StartTime.IsZero() {
			point.SetStartTimestamp(pcommon.NewTimestampFromTime(sumPoint.StartTime))
		}
		point.SetTimestamp(report.end)
		putAttrs(point.Attributes(), sumPoint.Attrs)
	}
}

func (report *Report) scopeMetrics(scopeMetrics pmetric.ScopeMetrics, problems []Problem) {
	points := gauge(scopeMetrics, MetricEksProblem, unitDimensionless)
	for _, problem := range problems {
		attrs := Attrs{ProblemAttrType: problem.Type, ProblemAttrReason: problem.Reason}
		for key, value := range problem.Resources {
			attrs[ProblemAttrResource+key] = value
		}
		if points.Len() == MaxPointsPerMetric || !validAttrs(attrs) {
			// TODO: count dropped points.
			continue
		}
		point := points.AppendEmpty()
		point.SetIntValue(1)
		point.SetTimestamp(report.end)
		putAttrs(point.Attributes(), attrs)
	}
}

// Marshal returns the report as a gzipped OTLP protobuf request.
func (report *Report) Marshal() ([]byte, error) {
	// TODO: bound the raw and gzipped size, dropping points by priority.
	raw, err := pmetricotlp.NewExportRequestFromMetrics(report.metrics).MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("marshal OTLP: %w", err)
	}
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	if _, err := gzipWriter.Write(raw); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newMetric(scopeMetrics pmetric.ScopeMetrics, name, unit string) pmetric.Metric {
	metric := scopeMetrics.Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetUnit(unit)
	return metric
}

func gauge(scopeMetrics pmetric.ScopeMetrics, name, unit string) pmetric.NumberDataPointSlice {
	return newMetric(scopeMetrics, name, unit).SetEmptyGauge().DataPoints()
}

func validAttrs(attrs Attrs) bool {
	for _, value := range attrs {
		if !attrValueRe.MatchString(value) {
			return false
		}
	}
	return true
}

func putAttrs(dst pcommon.Map, src Attrs) {
	for key, value := range src {
		dst.PutStr(key, value)
	}
}
