// Package nvidiasmi reads GPU fabric health and GPU processes from the host's
// nvidia-smi into a classify.FabricCrossCheck.
package nvidiasmi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/eks-node-monitoring-agent/monitors/nvidia/classify"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

// DefaultTimeout bounds one nvidia-smi run, which takes about a second on a
// healthy node.
const DefaultTimeout = 30 * time.Second

// ErrStillRunning is returned by Read while an earlier nvidia-smi has not
// exited.
var ErrStillRunning = errors.New("an earlier nvidia-smi run has not exited")

// Reader runs nvidia-smi one copy at a time, so a copy hung in the driver is
// not joined by a new one on every call. Use it from one goroutine at a time.
type Reader struct {
	root    string
	timeout time.Duration
	exited  <-chan struct{} // closed when the last run has exited; nil before the first
}

// NewReader returns a Reader for the agent's host root ("/host" in the
// DaemonSet, "/" when the agent runs on the host).
func NewReader() *Reader {
	return &Reader{root: config.HostRoot(), timeout: DefaultTimeout}
}

// binary returns the nvidia-smi to run: the one under the GPU Operator driver
// root when the driver is containerized there, otherwise the plain name
// resolved on PATH under the host root. This mirrors the driver-root search in
// package dcgm and nvml.
func (r *Reader) binary() string {
	if root := config.NvidiaDriverRoot(); root != "" {
		return filepath.Join(root, "usr/bin/nvidia-smi")
	}
	operatorSMI := filepath.Join(config.DefaultGPUOperatorDriverRoot, "usr/bin/nvidia-smi")
	if _, err := os.Stat(config.ToHostPath(operatorSMI)); err == nil {
		return operatorSMI
	}
	return "nvidia-smi"
}

// Read runs `nvidia-smi -q -x` and parses its output.
func (r *Reader) Read(ctx context.Context) (*classify.FabricCrossCheck, error) {
	if r.exited != nil {
		select {
		case <-r.exited:
		default:
			return nil, ErrStillRunning
		}
	}
	out, exited, err := osext.NewExec(r.root).OutputTracked(ctx, r.timeout, r.binary(), "-q", "-x")
	r.exited = exited
	if err != nil {
		return nil, fmt.Errorf("running nvidia-smi: %w", err)
	}
	return Parse(out)
}

// Parse reads the output of `nvidia-smi -q -x`. A GPU without a fabric health
// summary gets an empty HealthSummary, which is not Healthy.
func Parse(data []byte) (*classify.FabricCrossCheck, error) {
	var doc struct {
		XMLName xml.Name `xml:"nvidia_smi_log"`
		GPUs    []struct {
			BusID     string     `xml:"id,attr"`
			Summary   string     `xml:"fabric>health>summary"`
			Processes []struct{} `xml:"processes>process_info"`
		} `xml:"gpu"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing nvidia-smi output: %w", err)
	}
	if len(doc.GPUs) == 0 {
		return nil, errors.New("nvidia-smi listed no GPUs")
	}
	check := &classify.FabricCrossCheck{}
	for _, gpu := range doc.GPUs {
		check.GPUs = append(check.GPUs, classify.GPUFabricHealth{
			BusID:         gpu.BusID,
			HealthSummary: strings.TrimSpace(gpu.Summary),
		})
		if len(gpu.Processes) > 0 {
			check.ProcessesRunning = true
		}
	}
	return check, nil
}
