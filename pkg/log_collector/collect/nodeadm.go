package collect

import "errors"

type Nodeadm struct{}

func (m Nodeadm) Collect(acc *Accessor) error {
	return errors.Join(
		acc.CommandOutput([]string{"journalctl", "-u", "nodeadm-config"}, "nodeadm/nodeadm-config.log", CommandOptionsNone),
		acc.CommandOutput([]string{"journalctl", "-u", "nodeadm-run"}, "nodeadm/nodeadm-run.log", CommandOptionsNone),
	)
}
