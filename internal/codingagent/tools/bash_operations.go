// Shell process execution shared by the bash and powershell tools and user
// bash.
package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BashOperationsExecOptions configures one shell command execution.
type BashOperationsExecOptions struct {
	// OnData receives raw output bytes (stdout and stderr) as they arrive,
	// from one goroutine at a time. The callee may keep the slice.
	OnData func(data []byte)
	// Timeout is the requested timeout in seconds; nil means none.
	Timeout *float64
	// Env replaces the inherited environment when non-nil.
	Env []string
}

// BashOperationsResult is the outcome of one shell command.
type BashOperationsResult struct {
	ExitCode *int
	// ProcessGroup is the shell's process group, which descendants it
	// left running in the background still belong to; 0 when unknown.
	ProcessGroup int
}

// BashOperations executes shell commands for the shell tools and user bash.
type BashOperations interface {
	Exec(ctx context.Context, command, cwd string, options BashOperationsExecOptions) (BashOperationsResult, error)
}

// errBashAborted reports an aborted command.
var errBashAborted = errors.New("aborted")

// maxBashTimeoutMs is the largest accepted timeout.
const maxBashTimeoutMs = 2_147_483_647

// maxBashTimeoutSeconds is maxBashTimeoutMs in seconds.
const maxBashTimeoutSeconds = maxBashTimeoutMs / 1000.0

// resolveTimeoutMs validates an optional timeout in seconds.
func resolveTimeoutMs(timeout *float64) (time.Duration, bool, error) {
	if timeout == nil {
		return 0, false, nil
	}
	if math.IsNaN(*timeout) || math.IsInf(*timeout, 0) || *timeout <= 0 {
		return 0, false, errors.New("Invalid timeout: must be a finite number of seconds")
	}
	timeoutMs := *timeout * 1000
	if timeoutMs > maxBashTimeoutMs {
		return 0, false, errors.New("Invalid timeout: maximum is " + jsNumber(maxBashTimeoutSeconds) + " seconds")
	}
	return time.Duration(timeoutMs * float64(time.Millisecond)), true, nil
}

// LocalShellOperations runs commands locally through a resolved shell, with
// PowerShell's command wrapper as an optional hook.
type LocalShellOperations struct {
	ShellName    string
	ResolveShell func() (ShellConfig, error)
	// WrapCommand, when set, rewrites the command before execution.
	WrapCommand func(string) string
	// BinDir is prepended to PATH when the caller passes no environment.
	BinDir string
}

// NewLocalBashOperations runs commands locally through the configured shell
// path, read when a command runs, so an invalid shell path fails that command.
func NewLocalBashOperations(settings SettingsView, binDir string) *LocalShellOperations {
	return &LocalShellOperations{
		ShellName:    "bash",
		BinDir:       binDir,
		ResolveShell: func() (ShellConfig, error) { return GetShellConfig(settings) },
	}
}

// Exec runs command through the resolved shell, streaming output to
// opts.OnData, and waits for the shell (not its background descendants).
func (o *LocalShellOperations) Exec(ctx context.Context, command, cwd string, opts BashOperationsExecOptions) (BashOperationsResult, error) {
	if o.WrapCommand != nil {
		command = o.WrapCommand(command)
	}
	timeout, hasTimeout, err := resolveTimeoutMs(opts.Timeout)
	if err != nil {
		return BashOperationsResult{}, err
	}
	if ctx.Err() != nil {
		return BashOperationsResult{}, errBashAborted
	}
	shell, err := o.ResolveShell()
	if err != nil {
		return BashOperationsResult{}, err
	}
	if _, err := os.Stat(cwd); err != nil {
		return BashOperationsResult{}, fmt.Errorf("Working directory does not exist: %s\nCannot execute %s commands.", cwd, o.ShellName)
	}
	env := opts.Env
	if env == nil {
		env = GetShellEnv(o.BinDir)
	}

	commandFromStdin := shell.CommandTransport == "stdin"
	args := append([]string{}, shell.Args...)
	if !commandFromStdin {
		args = append(args, command)
	}
	cmd := exec.Command(shell.Path, args...)
	cmd.Dir = cwd
	cmd.Env = env
	if commandFromStdin {
		cmd.Stdin = strings.NewReader(command)
	}
	// The shell writes straight into an OS pipe (no exec copy goroutine), so
	// Wait returns when the shell exits even if a background descendant still
	// holds the write end. waitForStdioIdle then applies the stdio grace.
	pr, pw, err := os.Pipe()
	if err != nil {
		return BashOperationsResult{}, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	// Process group so abort and timeout reap descendants.
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return BashOperationsResult{}, err
	}
	_ = pw.Close()

	var (
		killOnce sync.Once
		timedOut atomic.Bool
	)
	kill := func() { killOnce.Do(func() { _ = killProcessGroup(cmd.Process) }) }
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		var timer <-chan time.Time
		if hasTimeout {
			t := time.NewTimer(timeout)
			defer t.Stop()
			timer = t.C
		}
		select {
		case <-ctx.Done():
			kill()
		case <-timer:
			timedOut.Store(true)
			kill()
		case <-stopWatch:
		}
	}()

	readDone := make(chan struct{})
	activity := make(chan struct{}, 1)
	var (
		acceptMu  sync.Mutex
		accepting = true
	)
	go func() {
		defer close(readDone)
		for {
			buf := make([]byte, 32*1024)
			n, readErr := pr.Read(buf)
			if n > 0 {
				acceptMu.Lock()
				if !accepting {
					acceptMu.Unlock()
					return
				}
				if opts.OnData != nil {
					opts.OnData(buf[:n])
				}
				acceptMu.Unlock()
				select {
				case activity <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	if !waitForStdioIdle(readDone, activity) {
		acceptMu.Lock()
		accepting = false
		acceptMu.Unlock()
	}
	_ = pr.Close()
	<-readDone
	close(stopWatch)
	<-watchDone

	if ctx.Err() != nil {
		return BashOperationsResult{}, errBashAborted
	}
	didTimeOut := timedOut.Load()
	if didTimeOut {
		return BashOperationsResult{}, errors.New("timeout:" + jsNumber(*opts.Timeout))
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return BashOperationsResult{}, waitErr
	}
	code := shellExitCode(cmd.ProcessState)
	return BashOperationsResult{ExitCode: &code, ProcessGroup: cmd.Process.Pid}, nil
}

// exitStdioGrace is how long output may stay idle after the shell exits.
const exitStdioGrace = 100 * time.Millisecond

// waitForStdioIdle keeps reading after the shell has exited until the output
// pipe closes, or until no data has arrived for exitStdioGrace (re-armed on every chunk). A
// background descendant that inherited the pipe therefore neither blocks the
// call nor truncates output it is still writing. It reports whether the pipe
// closed.
func waitForStdioIdle(readDone, activity <-chan struct{}) bool {
	timer := time.NewTimer(exitStdioGrace)
	defer timer.Stop()
	for {
		select {
		case <-readDone:
			return true
		case <-activity:
			timer.Reset(exitStdioGrace)
		case <-timer.C:
			return false
		}
	}
}
