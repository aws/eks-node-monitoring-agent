package osext

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// ErrTimeout is returned when a command does not complete within its timeout.
// Callers can use errors.Is to distinguish a bounded run that gave up from a
// command that ran and failed.
var ErrTimeout = errors.New("timed out")

// waitDelay bounds how long os/exec waits for the child's I/O pipes to close
// after the context is cancelled. Without it, a killed process whose stdout is
// held open by a grandchild keeps the collecting goroutine alive indefinitely.
const waitDelay = 5 * time.Second

// CommandFunc builds the command to run. Implementations must construct the
// command from the context they are handed (exec.CommandContext or
// execExt.CommandContext) so that cancellation reaches the child process.
type CommandFunc func(context.Context) *exec.Cmd

// Output runs a command bounded by timeout and returns its standard output.
func Output(ctx context.Context, timeout time.Duration, newCmd CommandFunc) ([]byte, error) {
	output, _, err := run(ctx, timeout, newCmd, false)
	return output, err
}

// OutputTracked is Output, plus a channel that is closed once the command has
// exited, which can be after a timeout returns (see run).
func OutputTracked(ctx context.Context, timeout time.Duration, newCmd CommandFunc) ([]byte, <-chan struct{}, error) {
	return run(ctx, timeout, newCmd, false)
}

// CombinedOutput runs a command bounded by timeout and returns its standard
// output and standard error interleaved.
func CombinedOutput(ctx context.Context, timeout time.Duration, newCmd CommandFunc) ([]byte, error) {
	output, _, err := run(ctx, timeout, newCmd, true)
	return output, err
}

// run executes the command and returns once it completes, the timeout elapses,
// or ctx is cancelled — whichever happens first.
//
// The command is collected on its own goroutine, which is abandoned rather than
// waited on when the bound is hit. That is deliberate: binding the context
// signals the child, but a process in uninterruptible sleep never dies, and
// cmd.Wait cannot return until it does. Waiting for the goroutine here would
// reintroduce exactly the unbounded block this function exists to prevent.
//
// An abandoned goroutine holds its command and output buffer until the child
// finally exits, if it ever does. Callers that run the same command repeatedly
// should therefore avoid launching a fresh copy while a previous one is still
// outstanding; the returned channel tells them when it is gone.
func run(ctx context.Context, timeout time.Duration, newCmd CommandFunc, combined bool) ([]byte, <-chan struct{}, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newCmd(runCtx)
	cmd.WaitDelay = waitDelay

	type result struct {
		output []byte
		err    error
	}
	// Buffered so the goroutine can always publish its result and exit, even
	// when nobody is left to receive it.
	done := make(chan result, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		var r result
		if combined {
			r.output, r.err = cmd.CombinedOutput()
		} else {
			r.output, r.err = cmd.Output()
		}
		done <- r
	}()

	select {
	case r := <-done:
		return r.output, exited, r.err
	case <-runCtx.Done():
		// Report the parent's error when it is the parent that ended, so that
		// shutdown is not reported as a command timeout.
		if err := ctx.Err(); err != nil {
			return nil, exited, fmt.Errorf("running %q: %w", commandName(cmd), err)
		}
		return nil, exited, fmt.Errorf("running %q: %w after %s", commandName(cmd), ErrTimeout, timeout)
	}
}

// commandName is a short identifier for a command, for use in error messages.
func commandName(cmd *exec.Cmd) string {
	if len(cmd.Args) > 0 {
		return cmd.Args[0]
	}
	return filepath.Base(cmd.Path)
}

// Output runs name with the given arguments under the configured host root,
// bounded by timeout, and returns its standard output.
func (a *execExt) Output(ctx context.Context, timeout time.Duration, name string, arg ...string) ([]byte, error) {
	return Output(ctx, timeout, func(ctx context.Context) *exec.Cmd {
		return a.CommandContext(ctx, name, arg...)
	})
}

// OutputTracked is Output plus the exit channel (see the package-level
// OutputTracked).
func (a *execExt) OutputTracked(ctx context.Context, timeout time.Duration, name string, arg ...string) ([]byte, <-chan struct{}, error) {
	return OutputTracked(ctx, timeout, func(ctx context.Context) *exec.Cmd {
		return a.CommandContext(ctx, name, arg...)
	})
}

// CombinedOutput runs name with the given arguments under the configured host
// root, bounded by timeout, and returns its output and standard error.
func (a *execExt) CombinedOutput(ctx context.Context, timeout time.Duration, name string, arg ...string) ([]byte, error) {
	return CombinedOutput(ctx, timeout, func(ctx context.Context) *exec.Cmd {
		return a.CommandContext(ctx, name, arg...)
	})
}
