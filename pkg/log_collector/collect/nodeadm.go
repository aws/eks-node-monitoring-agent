package collect

import "errors"

type Nodeadm struct{}

func (m Nodeadm) Collect(acc *Accessor) error {
	return errors.Join(
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "nodeadm-config"}, "nodeadm/nodeadm-config.log", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-o", "short-iso-precise", "-u", "nodeadm-run"}, "nodeadm/nodeadm-run.log", CommandOptionsNone),
	)
}
