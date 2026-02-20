package collect

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/aws"
)

type Instance struct{}

func (i *Instance) Collect(acc *Accessor) error {
	if acc.cfg.hasAnyTag(TagHybrid) {
		// skip collect for hybrid node as it can not access IMDS
		return nil
	}

	if err := acc.CopyFile("/var/lib/cloud/data/instance-id", "system/instance-id.txt"); err == nil {
		return nil
	}
	// fallback to IMDS if unable to copy from the instance FS
	bytes, err := aws.GetIMDSMetadata("/instance-id", acc.imds)
	if err != nil {
		return fmt.Errorf("failed to get instance-id metadata: %w", err)
	}
	bytes = append(bytes, '\n')
	return acc.WriteOutput("system/instance-id.txt", bytes)
}
