package diagnostic

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Settings struct {
	LogInterval       time.Duration
	ApiServerEndpoint string
}

// NOTE: the functions in this logger don't check the host root path, which is
// not necessary unless you plan to run the logging outside of EKS Auto.

// diagnosticLogger is a routine that writes system logs regarding node health
// to a file handle.
type diagnosticLogger struct {
	settings Settings
	writer   io.Writer
}

const (
	sectionMarker = "NMA::LOG"
)

func NewDiagnosticLogger(writer io.Writer, settings Settings) diagnosticLogger {
	if writer == nil {
		writer = os.Stdout
	}
	if settings.LogInterval <= 0 {
		settings.LogInterval = 5 * time.Minute
	}
	if settings.ApiServerEndpoint == "" {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err == nil {
			settings.ApiServerEndpoint = config.Host
		}
	}

	return diagnosticLogger{
		settings: settings,
		writer:   writer,
	}
}

func (l diagnosticLogger) Start(ctx context.Context) error {
	var producers = []struct {
		name string
		fn   func() []byte
	}{
		{name: "cpu", fn: cpuUsage},
		{name: "memory", fn: memoryUsage},
		{name: "disk", fn: diskUsage},
		{name: "interfaces", fn: listNetworkInterfaces},
		{name: "ipamd", fn: func() []byte { return tailx(ipamd(), 5000) }},
		{name: "apiserver", fn: func() []byte { return testAPIServerEndpoint(l.settings.ApiServerEndpoint) }},
		{name: "dmesg", fn: func() []byte { return dmesg() }},
		{name: "systemd", fn: func() []byte { return systemdStatus("containerd", "kubelet") }},
		{name: "kubelet", fn: func() []byte { return journalctl("kubelet") }},
		{name: "containerd", fn: func() []byte { return journalctl("containerd") }},
	}

	// to calculate the sections size that would best fit in the 68K buffer, we
	// compute the remaining buffer length after subtracting out sections in
	// known and/or stable sizes
	const bufferLength = 68_000
	const headerBytes = 40
	constLengthSections := []int{
		1503, // memory
		1505, // apiserver
		2900, // systemd
		5000, // ipamd
	}
	constSectionBytes := 0
	for _, length := range constLengthSections {
		constSectionBytes += length
	}
	freeBytes := bufferLength - (constSectionBytes + (len(producers) * headerBytes))
	sectionBytes := freeBytes / (len(producers) - len(constLengthSections))

	ticker := time.NewTicker(l.settings.LogInterval)
	defer ticker.Stop()

	for {
		for _, producer := range producers {
			timestamp := time.Now().UTC().Format(time.RFC3339)
			header := strings.Join([]string{sectionMarker, timestamp, producer.name}, "|")
			data := tailx(producer.fn(), sectionBytes)
			if _, err := fmt.Fprintf(l.writer, "%s\n%s\n", header, data); err != nil {
				log.FromContext(ctx).Error(err, "error logging to writer")
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			log.FromContext(ctx).Info("Stopping diagnostics logger")
			return ctx.Err()
		}
	}
}

func systemdStatus(services ...string) []byte {
	args := []string{"status", "--all", "-n", "0"}
	if out, err := exec.Command("systemctl", append(args, services...)...).Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call systemctl due to: %s", err))
	} else {
		return out
	}
}

func journalctl(unit string) []byte {
	if out, err := exec.Command("journalctl", "--unit", unit).Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call journalctl due to: %s", err))
	} else {
		return out
	}
}

func cpuUsage() []byte {
	if out, err := exec.Command("ps", "aux").Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call ps due to: %s", err))
	} else {
		return out
	}
}

func diskUsage() []byte {
	if out, err := exec.Command("df", "-T").Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call df due to: %s", err))
	} else {
		return out
	}
}

func memoryUsage() []byte {
	if out, err := os.ReadFile("/proc/meminfo"); err != nil {
		return []byte(fmt.Sprintf("failed to read /pro/meminfo due to: %s", err))
	} else {
		return out
	}
}

func listNetworkInterfaces() []byte {
	interfaces, err := net.Interfaces()
	if err != nil {
		return []byte(fmt.Sprintf("failed to get network interfaces due to: %s", err))
	}
	var out bytes.Buffer
	for _, i := range interfaces {
		addrs, _ := i.Addrs()
		fmt.Fprintf(&out, "%d %s", i.Index, i.Name)
		for _, addr := range addrs {
			fmt.Fprintf(&out, " %s", addr)
		}
		if len(i.HardwareAddr) > 0 {
			fmt.Fprintf(&out, " MAC: %s", i.HardwareAddr)
		}
		fmt.Fprintf(&out, " MTU: %d Flags: %s", i.MTU, i.Flags)
		fmt.Fprintln(&out)
	}
	return out.Bytes()
}

func dmesg() []byte {
	if out, err := exec.Command("dmesg").Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call dmesg due to: %s", err))
	} else {
		return out
	}
}

func ipamd() []byte {
	if out, err := os.ReadFile("/var/log/aws-routed-eni/ipamd.log"); err != nil {
		return []byte(fmt.Sprintf("failed to read ipamd.log due to: %s", err))
	} else {
		return out
	}
}

func testAPIServerEndpoint(host string) []byte {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// Skipping TLS verification is ok as we are just validating the
				// connectivity to the API server
				InsecureSkipVerify: true,
			},
		},
	}
	url := fmt.Sprintf("%s/livez?verbose", host)
	r, err := client.Get(url)
	if err != nil {
		return []byte(fmt.Sprintf("failed to make request endpoint due to: %s\n", err))
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return []byte(fmt.Sprintf("failed to read response body due to: %s\n", err))
	}
	return body
}

// tailx returns up to n bytes of the buffer truncated at the point where the
// last line feed exists (if one exists).
func tailx(buf []byte, n int) []byte {
	headOfWindow := max(0, len(buf)-n)
	if headOfWindow == 0 {
		return buf[headOfWindow:]
	}
	lfIndex := headOfWindow
	for {
		if lfIndex >= len(buf) {
			return buf[headOfWindow:]
		}
		if buf[lfIndex-1] == '\n' {
			return buf[lfIndex:]
		}
		lfIndex += 1
	}
}
