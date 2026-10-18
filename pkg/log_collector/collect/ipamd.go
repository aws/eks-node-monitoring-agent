package collect

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	merr = errors.Join(merr, ipamdDatastores(acc))
	return merr
}

// ipamdDatastores copies every regular file in the IPAMD state directory, not
// just ipam.json: newer VPC CNI versions keep additional datastore files there
// (e.g. per-ENI and prefix state). Sockets and subdirectories are skipped. A
// missing directory means the VPC CNI is not installed and is not an error.
func ipamdDatastores(acc *Accessor) error {
	var ipamdStateDir = "/var/run/aws-node"
	if acc.cfg.hasAnyTag(TagBottlerocket) {
		ipamdStateDir = "/run/aws-node"
	}
	entries, err := os.ReadDir(filepath.Join(acc.cfg.Root, ipamdStateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var merr error
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			merr = errors.Join(merr, err)
			continue
		}
		if !info.Mode().IsRegular() {
			// the directory also holds the ipamd unix sockets
			continue
		}
		merr = errors.Join(merr, acc.CopyFile(
			filepath.Join(acc.cfg.Root, ipamdStateDir, entry.Name()),
			filepath.Join("ipamd/", entry.Name()),
		))
	}
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
