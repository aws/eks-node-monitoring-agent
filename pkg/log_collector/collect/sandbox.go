package collect

import "errors"

type Sandbox struct{}

func (m Sandbox) Collect(acc *Accessor) error {
	return errors.Join(
		// TODO: Only applicable on AL2. Remove after EoL
		acc.CommandOutput([]string{"journalctl", "-u", "sandbox-image"}, "sandbox-image/sandbox-image-log.txt", CommandOptionsNone),
	)
}
