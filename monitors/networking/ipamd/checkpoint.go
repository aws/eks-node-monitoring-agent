// IGNORE TEST COVERAGE (the file is not unit testable)

package ipamd

import (
	"encoding/json"
	"os"

	"github.com/aws/amazon-vpc-cni-k8s/pkg/ipamd/datastore"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
)

func GetCheckpoint() (*datastore.CheckpointData, error) {
	bytes, err := os.ReadFile(config.ToHostPath("/var/run/aws-node/ipam.json"))
	if err != nil {
		// Handle distros like Bottlerocket where the path is different.
		bytes, err = os.ReadFile(config.ToHostPath("/run/aws-node/ipam.json"))
		if err != nil {
			return nil, err
		}
	}
	var checkpoint datastore.CheckpointData
	if err := json.Unmarshal(bytes, &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}
