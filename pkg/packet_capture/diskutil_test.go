package packet_capture

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckDiskSpace_ValidPath(t *testing.T) {
	dir := t.TempDir()
	usage, err := checkDiskSpace(dir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, usage, 0.0)
	assert.LessOrEqual(t, usage, 1.0)
}

func TestCheckDiskSpace_InvalidPath(t *testing.T) {
	_, err := checkDiskSpace("/nonexistent/path/that/does/not/exist")
	assert.Error(t, err)
}
