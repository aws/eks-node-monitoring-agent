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

// ipamdDatastoreNote is written instead of the datastores when there is
// nothing to copy, so the bundle says why rather than silently omitting them.
const ipamdDatastoreNote = "ipamd/datastores.txt"

// ipamdDatastores copies every regular file in the IPAMD state directory, not
// just ipam.json: IPAMD keeps one datastore per network card (ipam.json for
// card 0, ipam-nic-<N>.json for the others), so on multi-card instances
// ipam.json alone misses most of the IP state. Sockets, symlinks and
// subdirectories are skipped.
//
// A missing directory or a missing ipam.json is recorded in a note rather than
// returned as an error: nodes that use another CNI have no state directory, so
// an error would fire on every capture there, and an IPAMD that isn't running
// is already reported by the failed introspection requests.
func ipamdDatastores(acc *Accessor) error {
	var ipamdStateDir = "/var/run/aws-node"
	if acc.cfg.hasAnyTag(TagBottlerocket) {
		ipamdStateDir = "/run/aws-node"
	}
	entries, err := os.ReadDir(filepath.Join(acc.cfg.Root, ipamdStateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return acc.WriteOutput(ipamdDatastoreNote, []byte(ipamdStateDir+" is not present on this system (the VPC CNI is not installed, or IPAMD has not started).\n"))
		}
		return err
	}
	var merr error
	foundCheckpoint := false
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
		if entry.Name() == "ipam.json" {
			foundCheckpoint = true
		}
		merr = errors.Join(merr, acc.CopyFile(
			filepath.Join(acc.cfg.Root, ipamdStateDir, entry.Name()),
			filepath.Join("ipamd/", entry.Name()),
		))
	}
	if !foundCheckpoint {
		merr = errors.Join(merr, acc.WriteOutput(ipamdDatastoreNote, []byte("ipam.json is not present in "+ipamdStateDir+" (IPAMD has not written its checkpoint yet).\n")))
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
