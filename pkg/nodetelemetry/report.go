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

const (
	AttrNodeName       = "k8s.node.name"
	AttrHostID         = "host.id"
	AttrHostArch       = "host.arch"
	AttrHostType       = "host.type"
	AttrZoneID         = "aws.zone_id"
	AttrKubeletVersion = "k8s.kubelet.version"
	AttrComputeType    = "aws.eks.compute_type"
)

const (
	MetricEksProblem       = "eks_problem"
	MetricEksNotReporting  = "eks_not_reporting"
	ProblemAttrType        = "problem.type"
	ProblemAttrReason      = "problem.reason"
	ProblemAttrResource    = "resource."
	NotReportingAttrReason = "reason"
)

const MaxPointsPerMetric = 50

var attrValueRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,64}$`)

type NodeAttrs struct {
	NodeName       string
	HostID         string
	HostArch       string
	HostType       string
	ZoneID         string
	KubeletVersion string
	ComputeType    string
}

type Attrs map[string]string

type GaugePoint struct {
	Value float64
	Attrs Attrs
}

type SumPoint struct {
	Delta     float64
	Attrs     Attrs
	StartTime time.Time
}

type Metric struct {
	Name   string
	Unit   string
	Gauges []GaugePoint
	Sums   []SumPoint
}

type Problem struct {
	Type      string
	Reason    string
	Resources map[string]string
}

type Scope struct {
	Name         string
	Metrics      []Metric
	Problems     []Problem
	NotReporting string
}

type Report struct {
	md         pmetric.Metrics
	rm         pmetric.ResourceMetrics
	start, end pcommon.Timestamp
}

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

func (r *Report) AddScope(s Scope) {
	if len(s.Metrics) == 0 && len(s.Problems) == 0 && s.NotReporting == "" {
		return
	}
	sm := r.rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName(s.Name)
	if s.NotReporting != "" {
		dp := gauge(sm, MetricEksNotReporting, "1").AppendEmpty()
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
	dps := gauge(sm, MetricEksProblem, "1")
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
