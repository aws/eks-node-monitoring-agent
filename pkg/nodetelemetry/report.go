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
	md         pmetric.Metrics
	rm         pmetric.ResourceMetrics
	start, end pcommon.Timestamp
}

// NewReport returns an empty report for the interval from start to end.
func NewReport(node NodeAttrs, start, end time.Time) *Report {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	a := rm.Resource().Attributes()
	for k, v := range map[string]string{
		AttrNodeName:       node.NodeName,
		AttrHostID:         node.HostID,
		AttrHostArch:       node.HostArch,
		AttrHostType:       node.HostType,
		AttrZoneID:         node.ZoneID,
		AttrKubeletVersion: node.KubeletVersion,
		AttrComputeType:    node.ComputeType,
	} {
		if v != "" {
			a.PutStr(k, v)
		}
	}
	return &Report{
		md:    md,
		rm:    rm,
		start: pcommon.NewTimestampFromTime(start),
		end:   pcommon.NewTimestampFromTime(end),
	}
}

// AddScope adds the scope to the report, or nothing if it's empty. Points with
// invalid attribute values, or beyond MaxPointsPerMetric, are dropped.
func (r *Report) AddScope(s Scope) {
	if len(s.Metrics) == 0 && len(s.Problems) == 0 && s.NotReporting == "" {
		return
	}
	sm := r.rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName(s.Name)
	if s.NotReporting != "" {
		dp := gauge(sm, MetricEksNotReporting, unitDimensionless).AppendEmpty()
		dp.SetIntValue(1)
		dp.SetTimestamp(r.end)
		dp.Attributes().PutStr(NotReportingAttrReason, s.NotReporting)
		return
	}
	for _, m := range s.Metrics {
		switch {
		case m.Sums != nil && m.Gauges != nil:
			// each Metric becomes one OTLP metric, which can only have one data type so a
			// Metric must set either Gauges or Sums, not both
			// https://github.com/open-telemetry/opentelemetry-proto/blob/09769935deb8b2ebcf73ad1e2142b691d17bf70b/opentelemetry/proto/metrics/v1/metrics.proto#L201-L210
			// TODO: count toward dropped metrics.
		case m.Sums != nil:
			r.addSum(sm, m)
		case m.Gauges != nil:
			r.addGauge(sm, m)
		}
	}
	if len(s.Problems) > 0 {
		r.addProblems(sm, s.Problems)
	}
}

func (r *Report) addGauge(sm pmetric.ScopeMetrics, m Metric) {
	dps := gauge(sm, m.Name, m.Unit)
	for _, p := range m.Gauges {
		if dps.Len() == MaxPointsPerMetric || !validAttrs(p.Attrs) {
			// TODO: count dropped points.
			continue
		}
		dp := dps.AppendEmpty()
		dp.SetDoubleValue(p.Value)
		dp.SetTimestamp(r.end)
		putAttrs(dp.Attributes(), p.Attrs)
	}
}

func (r *Report) addSum(sm pmetric.ScopeMetrics, m Metric) {
	sum := newMetric(sm, m.Name, m.Unit).SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(true)
	dps := sum.DataPoints()
	for _, p := range m.Sums {
		if p.Delta == 0 || dps.Len() == MaxPointsPerMetric || !validAttrs(p.Attrs) {
			// TODO: count dropped points.
			continue
		}
		dp := dps.AppendEmpty()
		dp.SetDoubleValue(p.Delta)
		dp.SetStartTimestamp(r.start)
		if !p.StartTime.IsZero() {
			dp.SetStartTimestamp(pcommon.NewTimestampFromTime(p.StartTime))
		}
		dp.SetTimestamp(r.end)
		putAttrs(dp.Attributes(), p.Attrs)
	}
}

func (r *Report) addProblems(sm pmetric.ScopeMetrics, problems []Problem) {
	dps := gauge(sm, MetricEksProblem, unitDimensionless)
	for _, p := range problems {
		attrs := Attrs{ProblemAttrType: p.Type, ProblemAttrReason: p.Reason}
		for k, v := range p.Resources {
			attrs[ProblemAttrResource+k] = v
		}
		if dps.Len() == MaxPointsPerMetric || !validAttrs(attrs) {
			// TODO: count dropped points.
			continue
		}
		dp := dps.AppendEmpty()
		dp.SetIntValue(1)
		dp.SetTimestamp(r.end)
		putAttrs(dp.Attributes(), attrs)
	}
}

// Marshal returns the report as a gzipped OTLP protobuf request.
func (r *Report) Marshal() ([]byte, error) {
	// TODO: bound the raw and gzipped size, dropping points by priority.
	raw, err := pmetricotlp.NewExportRequestFromMetrics(r.md).MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("marshal OTLP: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newMetric(sm pmetric.ScopeMetrics, name, unit string) pmetric.Metric {
	m := sm.Metrics().AppendEmpty()
	m.SetName(name)
	m.SetUnit(unit)
	return m
}

func gauge(sm pmetric.ScopeMetrics, name, unit string) pmetric.NumberDataPointSlice {
	return newMetric(sm, name, unit).SetEmptyGauge().DataPoints()
}

func validAttrs(a Attrs) bool {
	for _, v := range a {
		if !attrValueRe.MatchString(v) {
			return false
		}
	}
	return true
}

func putAttrs(dst pcommon.Map, src Attrs) {
	for k, v := range src {
		dst.PutStr(k, v)
	}
}
