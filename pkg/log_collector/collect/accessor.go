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

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/go-logr/logr"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/osext"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util/file"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type Accessor struct {
	cfg    Config
	imds   *imds.Client
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
}

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

func NewAccessor(cfg Config) (*Accessor, error) {
	ctx := context.Background()
	awscfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config, %w", err)
	}
	return &Accessor{
		cfg:    cfg,
		imds:   imds.NewFromConfig(awscfg),
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

func (a *Accessor) Command(name string, arg ...string) *exec.Cmd {
	return osext.NewExec(a.cfg.Root).Command(name, arg...)
}

func (a *Accessor) CommandOutput(args []string, destination string, opts CommandOptions) error {
	var (
		output []byte
		err    error
	)
	command := a.Command(args[0], args[1:]...)
	if opts.is(CommandOptionsNoStderr) {
		output, err = command.Output()
	} else {
		output, err = command.CombinedOutput()
	}
	if err != nil {
		if opts.is(CommandOptionsIgnoreFailure) {
			a.logger.Info("ignoring command failure", "args", args, "output", string(output), "error", err)
			return nil
		}
		msgs := []string{err.Error()}
		if len(output) > 0 {
			msgs = append(msgs, string(output))
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			msgs = append(msgs, string(exitErr.Stderr))
		}
		for i := range msgs {
			msgs[i] = strings.TrimSpace(msgs[i])
		}
		return fmt.Errorf("executing command %q: %s", strings.Join(args, " "), strings.Join(msgs, ": "))
	}
	if opts.is(CommandOptionsAppend) {
		return a.appendOutput(destination, output)
	}
	return a.WriteOutput(destination, output)
}

func (a *Accessor) CopyFile(src string, dst string) error {
	dstFilename, err := a.constructDestFile(dst)
	if err != nil {
		return fmt.Errorf("constructing destination file, %w", err)
	}
	return copyFileRaw(src, dstFilename)
}

func (a *Accessor) CopyDir(src string, dst string) error {
	dstDirName, err := a.constructDestFile(dst)
	if err != nil {
		return fmt.Errorf("constructing destination directory, %w", err)
	}
	return copyRecursive(src, dstDirName)
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

func copyRecursive(srcDir string, dstDir string) error {
	err := os.MkdirAll(dstDir, 0o755)
	if err != nil {
		return fmt.Errorf("creating destination directory, %w", err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("reading directory %q, %w", srcDir, err)
	}

	for _, ent := range entries {
		srcPath := filepath.Join(srcDir, ent.Name())
		dstPath := filepath.Join(dstDir, ent.Name())
		if st, err := os.Stat(srcPath); err != nil {
			return fmt.Errorf("stating %q, %w", srcPath, err)
		} else if st.IsDir() {
			if err := copyRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFileRaw(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFileRaw(srcFilename string, dstFilename string) error {
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

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("copying %q to %q, %w", srcFile.Name(), dstFile.Name(), err)
	}
	return nil
}
