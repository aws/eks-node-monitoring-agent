// IGNORE TEST COVERAGE (the file is not unit testable)

package ipamd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/amazon-vpc-cni-k8s/pkg/ipamd/datastore"
)

const (
	EndpointEnis               = "enis"
	EndpointPods               = "pods"
	EndpointNetworkEnvSettings = "networkutils-env-settings"
	EndpointIpamdEnvSettings   = "ipamd-env-settings"
	EndpointEniConfigs         = "eni-configs"

	DefaultURL = "http://localhost:61679/v1/"

	// EnvIntrospectionURL overrides the introspection API base URL, for
	// environments where ipamd does not listen on localhost (e.g. bound to a
	// link-local address via the VPC CNI's INTROSPECTION_BIND_ADDRESS).
	EnvIntrospectionURL = "IPAMD_INTROSPECTION_URL"
)

func GetEndpoint(endpoint string) (*datastore.ENIInfos, error) {
	baseURL := DefaultURL
	if u := os.Getenv(EnvIntrospectionURL); u != "" {
		baseURL = u
	}
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	urlPath, err := url.JoinPath(baseURL, endpoint)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(urlPath)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var eniInfos datastore.ENIInfos
	if err := json.Unmarshal(body, &eniInfos); err != nil {
		return nil, err
	}
	return &eniInfos, nil
}
