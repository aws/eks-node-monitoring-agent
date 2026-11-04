package scrape

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

var errTooLarge = errors.New("scrape response too large")

// Labels of a scraped eks_problem.
const (
	problemLabelType           = "type"
	problemLabelReason         = "reason"
	problemLabelResourcePrefix = "resource_"
)

// Series is one series of a scraped metric, with all of its labels.
type Series struct {
	Labels map[string]string
	Value  float64
}

// Parsed is the allowlisted content of a scrape response.
type Parsed struct {
	// Series are the allowlisted metrics' series, by metric name.
	Series map[string][]Series
	// Problems are the active eks_problem points, if eks_problem is allowlisted.
	Problems []nodetelemetry.Problem
	// SawIdentity is whether the identity metric was in the response.
	SawIdentity bool
}

// Parse decodes a Prometheus exposition body of the given Content-Type, keeping
// only allowlisted metrics. It fails if the body is larger than maxBytes.
func Parse(contentType string, body io.Reader, allow map[string]config.MetricSpec, maxBytes int64, identity string) (Parsed, error) {
	format := expfmt.ResponseFormat(http.Header{"Content-Type": {contentType}})
	if format.FormatType() == expfmt.TypeUnknown {
		format = expfmt.NewFormat(expfmt.TypeTextPlain)
	}
	dec := expfmt.NewDecoder(&limitReader{reader: body, left: maxBytes}, format)
	res := Parsed{Series: map[string][]Series{}}
	for {
		var metricFamily dto.MetricFamily
		err := dec.Decode(&metricFamily)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return res, nil
			} else if errors.Is(err, errTooLarge) {
				return Parsed{}, errTooLarge
			}
			return Parsed{}, fmt.Errorf("decode exposition: %w", err)
		}
		name := metricFamily.GetName()
		if name == identity && len(metricFamily.Metric) > 0 {
			res.SawIdentity = true
		}
		spec, ok := allow[name]
		switch {
		case !ok:
		case name == nodetelemetry.MetricEksProblem:
			res.Problems = append(res.Problems, problems(&metricFamily)...)
		case name == nodetelemetry.MetricEksNotReporting:
			// NMA reports a component's not-reporting status itself: reports of this
			// from target agents are deliberately not handled.
		default:
			var series []Series
			sent := map[string]bool{}
			for _, metric := range metricFamily.Metric {
				if metricValue, ok := value(&metricFamily, metric); ok {
					metricLabels := labels(metric)
					sent[seriesKey("", kept(metricLabels, spec.Labels))] = true
					series = append(series, Series{Labels: metricLabels, Value: metricValue})
				}
			}
			// MaxSeries caps the series that are sent: source series that differ only
			// in labels outside the allowlist are combined into one, so they count once.
			// TODO: count metrics dropped for exceeding MaxSeries.
			if len(series) > 0 && len(sent) <= spec.MaxSeries {
				res.Series[name] = series
			}
		}
	}
}

func problems(metricFamily *dto.MetricFamily) []nodetelemetry.Problem {
	var out []nodetelemetry.Problem
	for _, metric := range metricFamily.Metric {
		if metricValue, ok := value(metricFamily, metric); !ok || metricValue != 1 {
			continue
		}
		problem := nodetelemetry.Problem{Resources: map[string]string{}}
		for labelName, labelValue := range labels(metric) {
			switch {
			case labelName == problemLabelType:
				problem.Type = labelValue
			case labelName == problemLabelReason:
				problem.Reason = labelValue
			case strings.HasPrefix(labelName, problemLabelResourcePrefix):
				problem.Resources[camelCase(strings.TrimPrefix(labelName, problemLabelResourcePrefix))] = labelValue
			}
		}
		if problem.Type != "" && problem.Reason != "" {
			out = append(out, problem)
		}
	}
	return out
}

func value(metricFamily *dto.MetricFamily, metric *dto.Metric) (float64, bool) {
	switch metricFamily.GetType() {
	case dto.MetricType_GAUGE:
		return metric.GetGauge().GetValue(), metric.Gauge != nil
	case dto.MetricType_COUNTER:
		return metric.GetCounter().GetValue(), metric.Counter != nil
	case dto.MetricType_UNTYPED:
		return metric.GetUntyped().GetValue(), metric.Untyped != nil
	}
	return 0, false
}

func labels(metric *dto.Metric) map[string]string {
	out := make(map[string]string, len(metric.Label))
	for _, pair := range metric.Label {
		out[pair.GetName()] = pair.GetValue()
	}
	return out
}

func camelCase(snakeCase string) string {
	parts := strings.Split(snakeCase, "_")
	for index := 1; index < len(parts); index++ {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, "")
}

type limitReader struct {
	reader io.Reader
	left   int64
}

func (limited *limitReader) Read(buffer []byte) (int, error) {
	if limited.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(buffer)) > limited.left+1 {
		buffer = buffer[:limited.left+1]
	}
	count, err := limited.reader.Read(buffer)
	limited.left -= int64(count)
	if limited.left < 0 {
		return count, errTooLarge
	}
	return count, err
}
