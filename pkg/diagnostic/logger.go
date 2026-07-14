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
	// maxTailReadBytes bounds how much of a large file is read into memory.
	// It is generously larger than any per-section console budget, so the
	// tail-trimmed output is unchanged while memory stays bounded.
	maxTailReadBytes = 256 * 1024
	// journalMaxLines bounds journalctl output; without a limit the entire
	// unit journal is buffered into memory each cycle and grows unbounded
	// over the node's lifetime. Chosen to comfortably exceed the section budget.
	journalMaxLines = "2000"
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
	// Bound the journal read with --lines: without it the entire unit journal
	// is buffered into memory each cycle and grows unbounded over the node's
	// lifetime. The output is tail-trimmed to the section budget anyway.
	if out, err := exec.Command("journalctl", "-o", "short-iso-precise", "--unit", unit, "--lines", journalMaxLines).Output(); err != nil {
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
	return readFileTail("/var/log/aws-routed-eni/ipamd.log", maxTailReadBytes)
}

// readFileTail returns up to the last n bytes of the file at path without
// reading the whole file into memory. Large logs (which grow over the node's
// lifetime) would otherwise be fully buffered on every cycle; the caller still
// trims the result to the section budget via tailx, so the emitted output is
// unchanged.
func readFileTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return []byte(fmt.Sprintf("failed to read %s due to: %s", path, err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return []byte(fmt.Sprintf("failed to stat %s due to: %s", path, err))
	}
	if info.Size() > n {
		if _, err := f.Seek(info.Size()-n, io.SeekStart); err != nil {
			return []byte(fmt.Sprintf("failed to seek %s due to: %s", path, err))
		}
	}
	buf, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return []byte(fmt.Sprintf("failed to read %s due to: %s", path, err))
	}
	return buf
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
