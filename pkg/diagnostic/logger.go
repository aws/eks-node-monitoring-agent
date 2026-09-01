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
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Settings struct {
	LogInterval       time.Duration
	ApiServerEndpoint string
	// CollectorTimeout bounds how long a single collector may take before its
	// section is emitted as a timeout notice and the cycle moves on. It should
	// stay comfortably below LogInterval so that a slow collector does not
	// consume the whole cycle. Defaults to defaultCollectorTimeout.
	CollectorTimeout time.Duration
}

// NOTE: the functions in this logger don't check the host root path, which is
// not necessary unless you plan to run the logging outside of EKS Auto.

// diagnosticLogger is a routine that writes system logs regarding node health
// to a file handle.
type diagnosticLogger struct {
	settings Settings
	writer   io.Writer
	// producers are built once and reused for every cycle so that they can
	// track whether a previous run of themselves is still outstanding.
	producers []*producer
	// sectionBytes is the per section budget for the console buffer.
	sectionBytes int
}

// producer emits the body of one console section.
type producer struct {
	name string
	fn   func(context.Context) []byte

	// running reports whether a previous cycle's run of this producer has not
	// returned yet. A collector blocked in uninterruptible sleep never returns,
	// so without this the loop would start a fresh copy every cycle and
	// accumulate stuck processes and goroutines for the life of the node.
	running atomic.Bool
	// startedAt is the unix nano timestamp of the outstanding run, and is only
	// meaningful while running is true.
	startedAt atomic.Int64
}

const (
	sectionMarker = "NMA::LOG"
	// defaultCollectorTimeout bounds a single collector. Collectors read host
	// state that can block indefinitely (a 'df' on an unresponsive NFS or EFS
	// mount is enough), and the cycle is sequential, so an unbounded collector
	// silently stops every later section as well.
	defaultCollectorTimeout = 30 * time.Second
	// maxTailReadBytes bounds how much of a large file is read into memory.
	// It is generously larger than any per-section console budget, so the
	// tail-trimmed output is unchanged while memory stays bounded.
	maxTailReadBytes = 256 * 1024
	// journalMaxLines bounds journalctl output; without a limit the entire
	// unit journal is buffered into memory each cycle and grows unbounded
	// over the node's lifetime. Chosen to comfortably exceed the section budget.
	journalMaxLines = "2000"

	// Pace console writes below the serial device's drain rate so a diagnostic
	// cycle cannot monopolize the line and stall co-located workloads' network
	// path. Set under the ~40 KB/s observed on a 2-vCPU node.
	consoleBytesPerSecond = 32 * 1024
	// Bound each contiguous write so no single write is large enough to stall
	// the line, independent of section size.
	consoleWriteChunk = 2 * 1024
)

// pacedWriter wraps an io.Writer (the /dev/console handle) and feeds bytes
// through at a fixed rate in small chunks, so a large diagnostic cycle is
// spread over time rather than written as one blocking burst.
type pacedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	chunk   int
}

// newPacedWriter returns a writer that hands bytes to w at bytesPerSecond in
// chunks of at most chunkBytes. The limiter's burst equals chunkBytes so the
// allowance never accumulates enough to permit a large instantaneous write.
func newPacedWriter(w io.Writer, bytesPerSecond, chunkBytes int) *pacedWriter {
	return &pacedWriter{
		w:       w,
		limiter: rate.NewLimiter(rate.Limit(bytesPerSecond), chunkBytes),
		chunk:   chunkBytes,
	}
}

// Write splits p into chunks and blocks before each one until the rate limiter
// permits it, so the aggregate throughput to the underlying writer stays at or
// below the configured bytes-per-second.
func (pw *pacedWriter) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		end := written + pw.chunk
		if end > len(p) {
			end = len(p)
		}
		// context.Background: this path has no cancellation signal, and a bounded
		// cycle stays well under the flush grace, so a paced write is not worth
		// making abortable.
		if err := pw.limiter.WaitN(context.Background(), end-written); err != nil {
			return written, err
		}
		n, err := pw.w.Write(p[written:end])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func NewDiagnosticLogger(writer io.Writer, settings Settings) diagnosticLogger {
	if writer == nil {
		writer = os.Stdout
	}
	// Rate-limit console writes; see consoleBytesPerSecond.
	writer = newPacedWriter(writer, consoleBytesPerSecond, consoleWriteChunk)
	if settings.LogInterval <= 0 {
		settings.LogInterval = 5 * time.Minute
	}
	if settings.CollectorTimeout <= 0 {
		settings.CollectorTimeout = defaultCollectorTimeout
	}
	if settings.ApiServerEndpoint == "" {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err == nil {
			settings.ApiServerEndpoint = config.Host
		}
	}

	producers := []*producer{
		{name: "cpu", fn: cpuUsage},
		{name: "memory", fn: memoryUsage},
		{name: "disk", fn: diskUsage},
		{name: "interfaces", fn: listNetworkInterfaces},
		{name: "ipamd", fn: func(context.Context) []byte { return tailx(ipamd(), 5000) }},
		{name: "apiserver", fn: func(context.Context) []byte { return testAPIServerEndpoint(settings.ApiServerEndpoint) }},
		{name: "dmesg", fn: dmesg},
		{name: "systemd", fn: func(ctx context.Context) []byte { return systemdStatus(ctx, "containerd", "kubelet") }},
		{name: "kubelet", fn: func(ctx context.Context) []byte { return journalctl(ctx, "kubelet") }},
		{name: "containerd", fn: func(ctx context.Context) []byte { return journalctl(ctx, "containerd") }},
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

	return diagnosticLogger{
		settings:     settings,
		writer:       writer,
		producers:    producers,
		sectionBytes: freeBytes / (len(producers) - len(constLengthSections)),
	}
}

func (l diagnosticLogger) Start(ctx context.Context) error {
	ticker := time.NewTicker(l.settings.LogInterval)
	defer ticker.Stop()

	for {
		l.RunOnce(ctx)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			log.FromContext(ctx).Info("Stopping diagnostics logger")
			return ctx.Err()
		}
	}
}

// RunOnce writes exactly one cycle of diagnostic sections and returns. Every
// section is emitted, whether or not its collector produced anything, and the
// cycle is bounded by CollectorTimeout per collector.
func (l diagnosticLogger) RunOnce(ctx context.Context) {
	for _, producer := range l.producers {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		header := strings.Join([]string{sectionMarker, timestamp, producer.name}, "|")
		data := tailx(l.collect(ctx, producer), l.sectionBytes)
		if _, err := fmt.Fprintf(l.writer, "%s\n%s\n", header, data); err != nil {
			log.FromContext(ctx).Error(err, "error logging to writer")
		}
	}
}

// collect returns the producer's output, or a notice to emit in its place when
// the producer does not finish in time. It never blocks for longer than
// CollectorTimeout, which is what keeps the cycle — and therefore every later
// section — making progress when a collector wedges.
//
// The collector runs on its own goroutine, which is abandoned rather than waited
// on once the bound is hit. The context handed to the collector is cancelled at
// the same deadline, so a killable child is terminated; a child in
// uninterruptible sleep is not, and its goroutine stays outstanding. That is
// what the running flag is for: while a run is outstanding the producer is
// skipped instead of started again, so a single permanently wedged collector
// costs one goroutine and one process rather than a fresh pair every cycle.
func (l diagnosticLogger) collect(ctx context.Context, p *producer) []byte {
	if !p.running.CompareAndSwap(false, true) {
		// zero means the winning caller has claimed the slot but has not stored its
		// start time yet, so there is no duration to report. Reporting one anyway
		// would measure from the epoch.
		if startedAt := p.startedAt.Load(); startedAt == 0 {
			return []byte(fmt.Sprintf("collector %q has not returned and is skipped until it does", p.name))
		} else {
			blocked := time.Since(time.Unix(0, startedAt)).Truncate(time.Second)
			return []byte(fmt.Sprintf(
				"collector %q has not returned after %s and is skipped until it does", p.name, blocked))
		}
	}
	// stored by the caller that claimed the slot, so the timestamp belongs to the
	// outstanding run. Storing it before the claim would let every skipped caller
	// overwrite it with the time of its own attempt, which reads back as zero
	// elapsed; the skip path above handles the not-yet-stored case instead.
	p.startedAt.Store(time.Now().UnixNano())

	collectCtx, cancel := context.WithTimeout(ctx, l.settings.CollectorTimeout)
	// cancelled here rather than by the collector goroutine. Cancelling from the
	// goroutine would make collectCtx.Done ready as soon as the collector
	// finished, so the select below would pick between a real result and a
	// spurious timeout at random.
	defer cancel()

	// buffered so that an abandoned collector can still publish and exit.
	done := make(chan []byte, 1)
	go func() {
		data := p.fn(collectCtx)
		// released before publishing, so that a caller which receives the result
		// always observes the producer as free again. Only the goroutine that
		// claimed the slot ever releases it, and only once, so an abandoned
		// collector cannot free a later run's claim: while it is outstanding the
		// flag stays set and every other caller is skipped without claiming.
		p.running.Store(false)
		done <- data
	}()

	select {
	case data := <-done:
		return data
	case <-collectCtx.Done():
		if err := ctx.Err(); err != nil {
			return []byte(fmt.Sprintf("collector %q did not finish before shutdown", p.name))
		}
		return []byte(fmt.Sprintf("collector %q timed out after %s", p.name, l.settings.CollectorTimeout))
	}
}

// commandOutput runs a command bound to ctx and returns its standard output, or
// the failure as the section body, matching the convention of reporting a
// collector's problem as its content rather than dropping the section.
//
// The command is run in the foreground and is deliberately not wrapped in
// osext.Output: the caller (diagnosticLogger.collect) already bounds this
// collector, and it needs the collector to stay outstanding for as long as its
// child does. Returning early here instead would let the next cycle start
// another copy of a command that can never be killed.
func commandOutput(ctx context.Context, name string, arg ...string) []byte {
	cmd := exec.CommandContext(ctx, name, arg...)
	// bound the wait for the child's pipes to close after it is killed, so that
	// a grandchild holding stdout open cannot keep this collector outstanding.
	cmd.WaitDelay = 5 * time.Second
	if out, err := cmd.Output(); err != nil {
		return []byte(fmt.Sprintf("failed to call %s due to: %s", name, err))
	} else {
		return out
	}
}

func systemdStatus(ctx context.Context, services ...string) []byte {
	args := []string{"status", "--all", "-n", "0"}
	return commandOutput(ctx, "systemctl", append(args, services...)...)
}

func journalctl(ctx context.Context, unit string) []byte {
	// Bound the journal read with --lines: without it the entire unit journal
	// is buffered into memory each cycle and grows unbounded over the node's
	// lifetime. The output is tail-trimmed to the section budget anyway.
	return commandOutput(ctx, "journalctl", "-o", "short-iso-precise", "--unit", unit, "--lines", journalMaxLines)
}

func cpuUsage(ctx context.Context) []byte {
	return commandOutput(ctx, "ps", "aux")
}

func diskUsage(ctx context.Context) []byte {
	return commandOutput(ctx, "df", "-T")
}

func memoryUsage(context.Context) []byte {
	if out, err := os.ReadFile("/proc/meminfo"); err != nil {
		return []byte(fmt.Sprintf("failed to read /pro/meminfo due to: %s", err))
	} else {
		return out
	}
}

func listNetworkInterfaces(context.Context) []byte {
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

func dmesg(ctx context.Context) []byte {
	return commandOutput(ctx, "dmesg")
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

// apiServerClient is built once and reused for every check. A client and
// transport per call would give each check its own connection pool, so every
// cycle would pay a fresh TCP connect and TLS handshake for the life of the node.
var apiServerClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			// Skipping TLS verification is ok as we are just validating the
			// connectivity to the API server
			InsecureSkipVerify: true,
		},
		// A hand-built transport leaves this at zero, which means no limit, so
		// idle connections would only ever be reclaimed by the peer closing them.
		// Matches http.DefaultTransport and the API server's own idle timeout.
		IdleConnTimeout: 90 * time.Second,
	},
}

func testAPIServerEndpoint(host string) []byte {
	url := fmt.Sprintf("%s/livez?verbose", host)
	r, err := apiServerClient.Get(url)
	if err != nil {
		return []byte(fmt.Sprintf("failed to make request endpoint due to: %s\n", err))
	}
	// the body must be closed for the connection to be released, whether or not
	// it is read successfully below.
	defer r.Body.Close()
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
