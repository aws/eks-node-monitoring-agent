package collect

import (
	"errors"
	"path/filepath"
)

type Nvidia struct{}

func (i *Nvidia) Collect(acc *Accessor) error {
	return errors.Join(
		nvidiaBugReport(acc),
	)
}

func nvidiaBugReport(acc *Accessor) error {
	// bottlerocket doesn't have the nvidia debug script
	if !acc.cfg.hasAnyTag(TagNvidia) || acc.cfg.hasAnyTag(TagBottlerocket) {
		return nil
	}
	return errors.Join(
		acc.CommandOutput([]string{"nvidia-bug-report.sh", "--output-file", "/tmp/nvidia-bug-report.log"}, "gpu/nvidia-bug-report-exec.log", CommandOptionsNone),
		// ".gz" is added by nvidia-bug-report.sh to the output file path.
		acc.CopyFile(filepath.Join(acc.cfg.Root, "/tmp/nvidia-bug-report.log.gz"), "gpu/nvidia-bug-report.log.gz"),
	)
}
