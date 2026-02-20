package osext

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

type RawSysctlParser[T any] func([]byte) (T, error)

func ParseSysctl[T any](sysctlName string, parser RawSysctlParser[T]) (*T, error) {
	sysctlPath := filepath.Join("/proc/sys/", strings.ReplaceAll(sysctlName, ".", "/"))
	sysctlRaw, err := os.ReadFile(config.ToHostPath(sysctlPath))
	if err != nil {
		return nil, err
	}
	// discard the extra newlines and whitespace in the file
	sysctlRaw = bytes.TrimSpace(sysctlRaw)
	// skip values that don't parse right
	sysctlValue, err := parser(sysctlRaw)
	if err != nil {
		return nil, err
	}
	return &sysctlValue, nil
}
