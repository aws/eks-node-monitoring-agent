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
	dec := expfmt.NewDecoder(&limitReader{r: body, left: maxBytes}, format)
	res := Parsed{Series: map[string][]Series{}}
	for {
		var mf dto.MetricFamily
		err := dec.Decode(&mf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return res, nil
			} else if errors.Is(err, errTooLarge) {
				return Parsed{}, errTooLarge
			}
			return Parsed{}, fmt.Errorf("decode exposition: %w", err)
		}
		name := mf.GetName()
		if name == identity && len(mf.Metric) > 0 {
			res.SawIdentity = true
		}
		spec, ok := allow[name]
		switch {
		case !ok:
		case name == nodetelemetry.MetricEksProblem:
			res.Problems = append(res.Problems, problems(&mf)...)
		case name == nodetelemetry.MetricEksNotReporting:
			// NMA reports a component's not-reporting status itself: reports of this
			// from target agents are deliberately not handled.
		default:
			var series []Series
			sent := map[string]bool{}
			for _, m := range mf.Metric {
				if v, ok := value(&mf, m); ok {
					l := labels(m)
					sent[seriesKey("", kept(l, spec.Labels))] = true
					series = append(series, Series{Labels: l, Value: v})
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

func problems(mf *dto.MetricFamily) []nodetelemetry.Problem {
	var out []nodetelemetry.Problem
	for _, m := range mf.Metric {
		if v, ok := value(mf, m); !ok || v != 1 {
			continue
		}
		p := nodetelemetry.Problem{Resources: map[string]string{}}
		for k, v := range labels(m) {
			switch {
			case k == problemLabelType:
				p.Type = v
			case k == problemLabelReason:
				p.Reason = v
			case strings.HasPrefix(k, problemLabelResourcePrefix):
				p.Resources[camelCase(strings.TrimPrefix(k, problemLabelResourcePrefix))] = v
			}
		}
		if p.Type != "" && p.Reason != "" {
			out = append(out, p)
		}
	}
	return out
}

func value(mf *dto.MetricFamily, m *dto.Metric) (float64, bool) {
	switch mf.GetType() {
	case dto.MetricType_GAUGE:
		return m.GetGauge().GetValue(), m.Gauge != nil
	case dto.MetricType_COUNTER:
		return m.GetCounter().GetValue(), m.Counter != nil
	case dto.MetricType_UNTYPED:
		return m.GetUntyped().GetValue(), m.Untyped != nil
	}
	return 0, false
}

func labels(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.Label))
	for _, lp := range m.Label {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

func camelCase(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

type limitReader struct {
	r    io.Reader
	left int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, errTooLarge
	}
	return n, err
}
