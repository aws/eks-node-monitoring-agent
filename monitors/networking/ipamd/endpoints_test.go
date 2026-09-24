package ipamd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/amazon-vpc-cni-k8s/pkg/ipamd/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntrospectionBaseURL(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv(EnvIntrospectionAddress, "")
		assert.Equal(t, "http://localhost:61679/v1/", introspectionBaseURL())
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv(EnvIntrospectionAddress, "169.254.1.1:61679")
		assert.Equal(t, "http://169.254.1.1:61679/v1/", introspectionBaseURL())
	})
}

func TestGetEndpointUsesOverride(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(datastore.ENIInfos{})
	}))
	defer server.Close()

	// server URL is http://host:port, the env override takes host:port only
	t.Setenv(EnvIntrospectionAddress, strings.TrimPrefix(server.URL, "http://"))

	_, err := GetEndpoint(EndpointEnis)
	require.NoError(t, err)
	assert.Equal(t, "/v1/enis", gotPath)
}
