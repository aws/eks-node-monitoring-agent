package nodetelemetry

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	shared := &http.Client{Transport: &http.Transport{}}
	client := NewClient(shared, "https://example.invalid/")
	assert.Equal(t, "https://example.invalid"+metricsAPIPath, client.url)
	assert.Equal(t, defaultSendTimeout, client.http.Timeout, "unset: the default applies")
	assert.Same(t, shared.Transport, client.http.Transport, "the transport is shared")
	assert.Zero(t, shared.Timeout, "the caller's client isn't modified")

	client = NewClient(&http.Client{Timeout: 5 * time.Second}, "https://example.invalid")
	assert.Equal(t, 5*time.Second, client.http.Timeout, "set: the caller's timeout is kept")
}
