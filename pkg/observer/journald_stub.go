//go:build !linux

package observer

import (
	"fmt"

	"golang.a2z.com/Eks-node-monitoring-agent/monitor/resource"
)

func init() {
	RegisterObserverConstructor(resource.ResourceTypeJournal, func(rp []resource.Part) (Observer, error) {
		return nil, fmt.Errorf("journal observer is only supported on Linux")
	})
}
