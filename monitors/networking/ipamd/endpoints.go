// IGNORE TEST COVERAGE (the file is not unit testable)

package ipamd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/amazon-vpc-cni-k8s/pkg/ipamd/datastore"
)

const (
	EndpointEnis               = "enis"
	EndpointPods               = "pods"
	EndpointNetworkEnvSettings = "networkutils-env-settings"
	EndpointIpamdEnvSettings   = "ipamd-env-settings"
	EndpointEniConfigs         = "eni-configs"

	Host = "http://localhost:61679/v1/"
)

func GetEndpoint(endpoint string) (*datastore.ENIInfos, error) {
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	urlPath, err := url.JoinPath(Host, endpoint)
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
