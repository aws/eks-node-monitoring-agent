package nodetelemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestNewClient_Timeout(t *testing.T) {
	c, err := NewClient(&rest.Config{Host: "https://example.invalid"})
	require.NoError(t, err)
	assert.Equal(t, defaultSendTimeout, c.http.Timeout, "unset: the default applies")

	cfg := &rest.Config{Host: "https://example.invalid", Timeout: 5 * time.Second}
	c, err = NewClient(cfg)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, c.http.Timeout, "set: the caller's timeout is kept")

	unset := &rest.Config{Host: "https://example.invalid"}
	_, err = NewClient(unset)
	require.NoError(t, err)
	assert.Zero(t, unset.Timeout, "the caller's config isn't modified")
}
