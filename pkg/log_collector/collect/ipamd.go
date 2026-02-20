package collect

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

type IPAMD struct{}

var introspectionDataPoints = []string{
	"enis",
	"pods",
	"networkutils-env-settings",
	"ipamd-env-settings",
	"eni-configs",
}

func (m IPAMD) Collect(acc *Accessor) error {
	var merr error
	if acc.cfg.hasAnyTag(TagHybrid) {
		// skip collect for hybrid node as it can not access the endpoint
		// IPAMD is installed by VPC CNI, which does not exist for hybrid nodes
		return nil
	}

	for _, entry := range introspectionDataPoints {
		if resp, err := collectEndpoint("http://localhost:61679/v1/", entry); err != nil {
			merr = errors.Join(merr, err)
		} else {
			merr = errors.Join(merr, acc.WriteOutput(fmt.Sprintf("ipamd/%s.json", entry), resp))
		}
	}
	if resp, err := collectEndpoint("http://localhost:61678/", "metrics"); err != nil {
		merr = errors.Join(merr, err)
	} else {
		merr = errors.Join(merr, acc.WriteOutput("ipamd/metrics.json", resp))
	}
	// collect ipamd checkpoint
	var ipamdCheckpointPath = "/var/run/aws-node/ipam.json"
	if acc.cfg.hasAnyTag(TagBottlerocket) {
		ipamdCheckpointPath = "/run/aws-node/ipam.json"
	}
	merr = errors.Join(merr, acc.CopyFile(filepath.Join(acc.cfg.Root, ipamdCheckpointPath), "ipamd/ipam.json"))
	return merr
}

func collectEndpoint(host string, entry string) ([]byte, error) {
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	urlPath, err := url.JoinPath(host, entry)
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
	return body, nil
}
