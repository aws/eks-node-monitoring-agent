package collect

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/file"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type Accessor struct {
	cfg  Config
	imds *imds.Client
	// ctx bounds the lifetime of the commands run through this accessor.
	// Collectors take no context of their own, so it is held here rather than
	// threaded through every Collect implementation.
	ctx    context.Context
	logger logr.Logger
}

type Config struct {
	// Root is the path to consider the filesystem root. This will affect the
	// environment that commands are executed and how paths are constructed.
	Root string
	// Destination is a directory to store the artifacts from Collectors.
	Destination string
	// Tags are used to provide context to Collectors about what tasks may or
	// may not be applicable to the current instance.
	Tags []string
	// CommandTimeout bounds each command a Collector runs. Defaults to
	// DefaultCommandTimeout.
	CommandTimeout time.Duration
}

// DefaultCommandTimeout bounds a single collector command. Collectors run
// commands that read host state and can block indefinitely rather than fail:
// 'df' and 'du' against an unresponsive NFS or EFS mount, 'ps' against a task
// stuck in the kernel, or 'journalctl' against a wedged journal. It is set well
// above what any of these commands legitimately need, so that hitting it means
// the command is stuck rather than slow.
const DefaultCommandTimeout = 60 * time.Second

const (
	TagNvidia       = "nvidia"
	TagBottlerocket = "bottlerocket"
	TagEKSAuto      = "eks-auto"
	TagHybrid       = "eks-hybrid"
)

func (c *Config) hasAnyTag(tags ...string) bool {
	for _, tag := range tags {
		if slices.Contains(c.Tags, tag) {
			return true
		}
	}
	return false
}

// NewAccessor builds an Accessor for a single collection run. Commands executed
// through it are bound to ctx, so cancelling ctx stops the collection.
func NewAccessor(ctx context.Context, cfg Config) (*Accessor, error) {
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = DefaultCommandTimeout
	}
	awscfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config, %w", err)
	}
	return &Accessor{
		cfg:    cfg,
		imds:   imds.NewFromConfig(awscfg),
		ctx:    ctx,
		logger: zap.New().WithName("log-collector"),
	}, nil
}

func (a *Accessor) WriteOutput(filename string, bytes []byte) error {
	destFile, err := a.constructDestFile(filename)
	if err != nil {
		return fmt.Errorf("constructing destination file, %w", err)
	}
	f, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("creating %q, %w", destFile, err)
	}
	defer f.Close()
	n, err := f.Write(bytes)
	if n != len(bytes) {
		return fmt.Errorf("short write, wrote %d of %d", n, len(bytes))
	}
	return err
}

func (a *Accessor) appendOutput(filename string, bytes []byte) error {
	destFile, err := a.constructDestFile(filename)
	if err != nil {
		return fmt.Errorf("constructing destination file, %w", err)
	}
	f, err := os.OpenFile(destFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating %q, %w", destFile, err)
	}
	defer f.Close()
	n, err := f.Write(bytes)
	if n != len(bytes) {
		return fmt.Errorf("short write, wrote %d of %d", n, len(bytes))
	}
	return err
}

type CommandOptions byte

func (o CommandOptions) is(opt CommandOptions) bool {
	return o&opt != 0
}

const (
	CommandOptionsNone          = 0
	CommandOptionsIgnoreFailure = 1 << (iota - 1)
	CommandOptionsAppend
	CommandOptionsNoStderr
)

// Output runs a command bounded by the configured CommandTimeout and returns its
// standard output.
func (a *Accessor) Output(args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no command given")
	}
	return osext.Output(a.ctx, a.cfg.CommandTimeout, a.newCommand(nil, args))
}

// CombinedOutput runs a command bounded by the configured CommandTimeout and
// returns its standard output and standard error.
func (a *Accessor) CombinedOutput(args ...string) ([]byte, error) {
	return a.CombinedOutputEnv(nil, args...)
}

// CombinedOutputEnv is CombinedOutput with an explicit child environment, which
// a command needs when it must not inherit part of the agent's environment.
func (a *Accessor) CombinedOutputEnv(env []string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no command given")
	}
	return osext.CombinedOutput(a.ctx, a.cfg.CommandTimeout, a.newCommand(env, args))
}

// newCommand builds the command under the configured root. Commands are always
// bound to a context: a collector that blocks forever leaves the whole capture
// stuck in Running, with no error for the caller to report.
func (a *Accessor) newCommand(env []string, args []string) osext.CommandFunc {
	return func(ctx context.Context) *exec.Cmd {
		cmd := osext.NewExec(a.cfg.Root).CommandContext(ctx, args[0], args[1:]...)
		if env != nil {
			cmd.Env = env
		}
		return cmd
	}
}

func (a *Accessor) CommandOutput(args []string, destination string, opts CommandOptions) error {
	var (
		output []byte
		err    error
	)
	if opts.is(CommandOptionsNoStderr) {
		output, err = a.Output(args...)
	} else {
		output, err = a.CombinedOutput(args...)
	}
	if err != nil {
		if opts.is(CommandOptionsIgnoreFailure) {
			a.logger.Info("ignoring command failure", "args", args, "output", string(output), "error", err)
			return nil
		}
		// the command's own output is detail rather than part of the error chain,
		// while err itself is wrapped, so that a caller can still tell a timeout
		// apart from a command that ran and failed.
		var details []string
		if len(output) > 0 {
			details = append(details, strings.TrimSpace(string(output)))
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			details = append(details, strings.TrimSpace(string(exitErr.Stderr)))
		}
		wrapped := fmt.Errorf("executing command %q: %w", strings.Join(args, " "), err)
		if len(details) > 0 {
			wrapped = fmt.Errorf("%w: %s", wrapped, strings.Join(details, ": "))
		}
		return wrapped
	}
	if opts.is(CommandOptionsAppend) {
		return a.appendOutput(destination, output)
	}
	return a.WriteOutput(destination, output)
}

// CopyFile copies a host file into the capture directory, bounded by the
// accessor context.
//
// The bound is cooperative: the open and each read are ordinary blocking
// syscalls, so a source on an unresponsive mount can still block inside one of
// them. What the context buys is that the copy stops at the first read boundary
// after cancellation instead of running to completion, and that a large file
// does not outlive the capture it belongs to. The caller
// (nodeDiagnosticController.collectLogsBounded) is what bounds a copy already
// stuck in a syscall, by abandoning the collection goroutine.
func (a *Accessor) CopyFile(src string, dst string) error {
	if err := a.ctx.Err(); err != nil {
		return fmt.Errorf("copying %q, %w", src, err)
	}
	dstFilename, err := a.constructDestFile(dst)
	if err != nil {
		return fmt.Errorf("constructing destination file, %w", err)
	}
	return copyFileRaw(a.ctx, src, dstFilename)
}

// CopyDir copies a host directory tree into the capture directory, bounded by
// the accessor context. See CopyFile for what the bound does and does not cover.
//
// The context is checked before each directory is read and before each entry, as
// well as per read, so a cancelled capture stops descending promptly rather than
// visiting every remaining entry first.
func (a *Accessor) CopyDir(src string, dst string) error {
	if err := a.ctx.Err(); err != nil {
		return fmt.Errorf("copying %q, %w", src, err)
	}
	dstDirName, err := a.constructDestFile(dst)
	if err != nil {
		return fmt.Errorf("constructing destination directory, %w", err)
	}
	return copyRecursive(a.ctx, src, dstDirName)
}

func (a *Accessor) constructDestFile(filename string) (string, error) {
	destFile := filepath.Join(a.cfg.Destination, filename)
	if !strings.HasPrefix(destFile, a.cfg.Destination) {
		return "", fmt.Errorf("invalid relative filename, %q", filename)
	}
	if err := file.EnsureParentExists(destFile, 0o755); err != nil {
		return "", fmt.Errorf("ensuring parent exists, %w", err)
	}
	return destFile, nil
}

func copyRecursive(ctx context.Context, srcDir string, dstDir string) error {
	// checked before MkdirAll and ReadDir, not just per entry: ReadDir is itself a
	// call that blocks on an unresponsive mount, and creating the destination
	// first would leave an empty directory behind that reads as a genuinely empty
	// log directory in the bundle.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("copying %q, %w", srcDir, err)
	}
	err := os.MkdirAll(dstDir, 0o755)
	if err != nil {
		return fmt.Errorf("creating destination directory, %w", err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("reading directory %q, %w", srcDir, err)
	}

	for _, ent := range entries {
		// re-checked per entry so a cancellation mid-walk stops here rather than
		// visiting every remaining entry first.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("copying %q, %w", srcDir, err)
		}
		srcPath := filepath.Join(srcDir, ent.Name())
		dstPath := filepath.Join(dstDir, ent.Name())
		if st, err := os.Stat(srcPath); err != nil {
			return fmt.Errorf("stating %q, %w", srcPath, err)
		} else if st.IsDir() {
			if err := copyRecursive(ctx, srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFileRaw(ctx, srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFileRaw(ctx context.Context, srcFilename string, dstFilename string) error {
	srcFile, err := os.Open(srcFilename)
	if err != nil {
		return fmt.Errorf("opening source file, %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dstFilename)
	if err != nil {
		return fmt.Errorf("creating destination file, %w", err)
	}
	defer dstFile.Close()

	// io.Copy over a context aware reader: the check lands between reads, so a
	// cancelled capture stops at the next chunk boundary instead of copying a
	// large file to the end. A read already blocked in the kernel is not
	// interrupted by this.
	_, err = io.Copy(dstFile, &ctxReader{ctx: ctx, r: srcFile})
	if err != nil {
		return fmt.Errorf("copying %q to %q, %w", srcFile.Name(), dstFile.Name(), err)
	}
	return nil
}

// ctxReader fails a read once its context is done, so that an io.Copy of a large
// or slow source stops at a chunk boundary after cancellation.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
