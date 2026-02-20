package collect

import (
	"errors"

	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/aws"
)

type Region struct{}

func (r *Region) Collect(acc *Accessor) error {
	var merr error

	if acc.cfg.hasAnyTag(TagHybrid) {
		// skip collect for hybrid node as it can not access IMDS
		return nil
	}

	region, err := aws.GetIMDSMetadata("/placement/region", acc.imds)
	if err != nil {
		merr = errors.Join(merr, err)
	} else {
		region = append(region, '\n')
		merr = errors.Join(merr, acc.WriteOutput("system/region.txt", region))
	}

	az, err := aws.GetIMDSMetadata("/placement/availability-zone", acc.imds)
	if err != nil {
		merr = errors.Join(merr, err)
	} else {
		az = append(az, '\n')
		merr = errors.Join(merr, acc.WriteOutput("system/availability-zone.txt", az))
	}

	return merr
}
