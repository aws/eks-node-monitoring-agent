package collect

import (
	"errors"
)

type AutoMode struct{}

var _ Collector = (*CommonLogs)(nil)

func (c *AutoMode) Collect(acc *Accessor) error {
	if !acc.cfg.hasAnyTag(TagEKSAuto) {
		// skip collection for non-Auto nodes which do not have eks-healthchecker		
		return nil
	}
	return errors.Join(
		acc.CommandOutput([]string{"journalctl", "-u", "eks-healthchecker"}, "system/eks-healthchecker.txt", CommandOptionsNone),
	)
}
