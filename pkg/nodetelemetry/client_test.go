package nodetelemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

func TestClient_Send(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   nodetelemetry.Outcome
	}{
		{http.StatusOK, nodetelemetry.Accepted},
		{http.StatusBadRequest, nodetelemetry.Dropped},
		{http.StatusNotFound, nodetelemetry.Dropped},
		{http.StatusTooManyRequests, nodetelemetry.Hold},
		{http.StatusInternalServerError, nodetelemetry.Hold},
		{http.StatusServiceUnavailable, nodetelemetry.Hold},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var got *http.Request
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, body = r, mustRead(t, r.Body)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("why"))
			}))
			t.Cleanup(srv.Close)

			c := nodetelemetry.NewClientFromURL(srv.Client(), srv.URL+"/apis/telemetry.eks.amazonaws.com/v1/metrics")
			r := nodetelemetry.NewReport(sampleNode, time.Unix(0, 0), time.Unix(300, 0))
			r.AddScope(nodetelemetry.Scope{Name: "ipamd", Metrics: []nodetelemetry.Metric{{Name: "assigned", Unit: "1", Gauges: []nodetelemetry.GaugePoint{{Value: 4}}}}})
			res, err := c.Send(context.Background(), r)
			require.NoError(t, err)

			assert.Equal(t, tc.want, res.Outcome)
			assert.Equal(t, tc.status, res.Status)
			assert.Equal(t, "why", res.Message)
			assert.Equal(t, http.MethodPost, got.Method)
			assert.Equal(t, "/apis/telemetry.eks.amazonaws.com/v1/metrics", got.URL.Path)
			assert.Equal(t, "application/x-protobuf", got.Header.Get("Content-Type"))
			assert.Equal(t, "gzip", got.Header.Get("Content-Encoding"))
			metricNamed(t, decode(t, body), "ipamd", "assigned")
		})
	}
}

func TestClient_TransportErrorHolds(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c := nodetelemetry.NewClientFromURL(srv.Client(), srv.URL)
	res, err := c.Send(context.Background(), nodetelemetry.NewReport(sampleNode, time.Now(), time.Now()))
	require.NoError(t, err)
	assert.Equal(t, nodetelemetry.Hold, res.Outcome)
}

func mustRead(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return b
}
