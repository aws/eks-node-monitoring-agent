package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSettingsDefaults(t *testing.T) {
	t.Setenv("STAGE", "beta")
	t.Setenv("EFS_FILESYSTEM_ID", "fs-0123")
	s, err := loadSettings()
	require.NoError(t, err)
	assert.Equal(t, "nma-soak", s.namespace)
	assert.Equal(t, "/nma-soak/beta/request", s.requestParam)
	assert.Equal(t, "/nma-soak/beta/result", s.resultParam)
	assert.Equal(t, 5*time.Hour, s.window)
	assert.Equal(t, 30*time.Minute, s.rolloutTimeout)
	assert.True(t, s.thresholds.RecordOnly, "the built-in thresholds start record-only")
}

func TestLoadSettingsRequiresStageAndEFS(t *testing.T) {
	t.Setenv("STAGE", "")
	t.Setenv("EFS_FILESYSTEM_ID", "")
	_, err := loadSettings()
	assert.ErrorContains(t, err, "STAGE is required")
	assert.ErrorContains(t, err, "EFS_FILESYSTEM_ID is required")
}
