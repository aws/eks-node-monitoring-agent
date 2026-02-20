package observer

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

func init() {
	RegisterObserverConstructor(resource.ResourceTypeDmesg, func(rp []resource.Part) (Observer, error) {
		if l := len(rp); l != 0 {
			return nil, fmt.Errorf("part count must be 0, but was %d", l)
		}
		// Use file observer over /dev/kmsg for dmesg
		// This is more straightforward when hostRoot is different
		return &fileObserver{path: config.ToHostPath("/dev/kmsg")}, nil
	})
}
