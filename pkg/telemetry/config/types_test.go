package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	monitorconfig "github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

func load(t *testing.T, path string) *config.Config {
	t.Helper()
	mc, _, err := monitorconfig.LoadMonitorConfig(path, nil)
	require.NoError(t, err)
	return mc.Telemetry
}

func loadString(t *testing.T, data string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
	return load(t, path)
}

func TestAutoModeExample(t *testing.T) {
	c := load(t, "testdata/auto-mode.yaml")

	assert.True(t, c.IsEnabled())
	assert.Equal(t, 5*time.Minute, c.ReportInterval())
	assert.Equal(t, 2, c.NotReportingThreshold())
	require.Len(t, c.Targets, 3)

	ipamd := c.Targets[0]
	assert.Equal(t, "ipamd", ipamd.Component)
	assert.Equal(t, config.TargetEndpoint{Address: "127.0.0.1", Port: 61678}, *ipamd.Endpoint)
	assert.Equal(t, "awscni_build_info", ipamd.Identity.RequireMetric)
	assert.Equal(t, config.MetricSpec{Name: "awscni_aws_api_error_count", Type: config.MetricTypeCounter, Unit: "1", Labels: []string{"api"}}, ipamd.Metrics[1])
	assert.Nil(t, c.Targets[2].Endpoint, "node-monitoring-agent is read in process")
}

func TestAddonExample(t *testing.T) {
	c := load(t, "testdata/addon.yaml")
	assert.True(t, c.IsEnabled())
	assert.Equal(t, config.ExpectedOnceSeen, c.TargetDefaults.Expected)
}

func TestNoTelemetryBlock(t *testing.T) {
	c := loadString(t, "monitors: {}\n")
	assert.Nil(t, c)
	assert.False(t, c.IsEnabled())
}

func TestResolve(t *testing.T) {
	c := loadString(t, `
telemetry:
  enabled: true
  targetDefaults: { timeout: 5s, maxSeries: 20, maxBytes: 2Mi }
  targets:
    - component: a
      maxSeries: 7
`)
	assert.Equal(t, config.Resolved{
		Path:      config.DefaultTargetPath,
		Timeout:   5 * time.Second,
		MaxBytes:  2 << 20,
		Expected:  config.DefaultExpectedPolicy,
		MaxSeries: 7,
	}, c.Resolve(c.Targets[0]), "target, then targetDefaults, then package defaults")
	assert.Equal(t, config.DefaultReportInterval, c.ReportInterval())
}

func TestValidate_DisabledBlockMustBeEmpty(t *testing.T) {
	for _, tc := range []struct {
		yaml    string
		wantErr bool
	}{
		{"telemetry: {enabled: false}\n", false},
		{"telemetry: {}\n", false},
		{"telemetry: {enabled: true, report: {interval: 1m}}\n", false},
		{"telemetry: {enabled: false, report: {interval: 1m}}\n", true},
		{"telemetry: {targets: [{component: a}]}\n", true},
		{"telemetry: {targetDefaults: {maxBytes: 1Mi}}\n", true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(tc.yaml), 0o644))
		_, _, err := monitorconfig.LoadMonitorConfig(path, nil)
		if tc.wantErr {
			assert.ErrorContains(t, err, "only be set when enabled is true", tc.yaml)
		} else {
			assert.NoError(t, err, tc.yaml)
		}
	}
}
