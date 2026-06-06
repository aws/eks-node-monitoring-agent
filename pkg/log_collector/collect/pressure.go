package collect

import (
	"errors"
	"os"
	"path/filepath"
)

// Pressure collects Linux Pressure Stall Information (PSI) snapshots from
// /proc/pressure/{cpu,memory,io} for inclusion in the diagnostic log bundle.
// PSI may be unavailable on older cgroup-v1 kernels or when disabled via the
// kernel boot parameter psi=0; missing files are skipped silently rather than
// failing the bundle.
type Pressure struct{}

var _ Collector = (*Pressure)(nil)

// pressureResources is the fixed set of PSI files exposed under /proc/pressure
// on Linux kernels with PSI enabled.
var pressureResources = []string{"cpu", "memory", "io"}

// Collect copies the raw PSI files into the bundle under system/. Each file is
// captured independently so a missing or unreadable resource does not abort
// collection of the others.
func (p *Pressure) Collect(acc *Accessor) error {
	var merr error
	for _, name := range pressureResources {
		merr = errors.Join(merr, capturePressure(acc, name))
	}
	return merr
}

func capturePressure(acc *Accessor, name string) error {
	src := filepath.Join(acc.cfg.Root, "/proc/pressure/", name)
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return acc.WriteOutput("system/pressure_"+name+".txt", data)
}
