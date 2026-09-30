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

	DefaultAddress = "localhost:61679"

	// EnvIntrospectionAddress overrides the introspection API address
	// (host:port), for environments where ipamd does not listen on localhost
	// (e.g. bound to a link-local address via the VPC CNI's
	// INTROSPECTION_BIND_ADDRESS). Matches the shape of the VPC CNI value so
	// it can be copied verbatim.
	EnvIntrospectionAddress = "IPAMD_INTROSPECTION_ADDRESS"
)

// introspectionBaseURL builds the API base URL, keeping the scheme and path
// internal so the env override stays address-shaped (host:port).
func introspectionBaseURL() string {
	address := os.Getenv(EnvIntrospectionAddress)
	if address == "" {
		address = DefaultAddress
	}
	return "http://" + address + "/v1/"
}

func GetEndpoint(endpoint string) (*datastore.ENIInfos, error) {
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	urlPath, err := url.JoinPath(introspectionBaseURL(), endpoint)
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
	defer resp.Body.Close()
	var eniInfos datastore.ENIInfos
	if err := json.Unmarshal(body, &eniInfos); err != nil {
		return nil, err
	}
	return &eniInfos, nil
}
