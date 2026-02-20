//go:build !linux

package observer

import (
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
)

func init() {
	RegisterObserverConstructor(resource.ResourceTypeJournal, func(rp []resource.Part) (Observer, error) {
		return nil, fmt.Errorf("journal observer is only supported on Linux")
	})
}
