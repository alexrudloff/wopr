package evals

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// ProcResult is one finished process: exit code, wall and CPU time, the
// peak RSS of the largest process in its tree, and its output.
type ProcResult struct {
	Code     int
	Wall     time.Duration
	CPU      time.Duration
	RSSMiB   float64
	Stdout   string
	Stderr   string
	TimedOut bool
}

// RunProc runs argv in dir with env, killing its whole process group after
// timeout.
func RunProc(argv, env []string, dir string, timeout time.Duration) ProcResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = env, dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { killProcessGroup(cmd); return nil }
	// A helper that inherited stdout must not hold Wait open forever.
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Start()
	if err != nil {
		return ProcResult{Code: 127, Stderr: err.Error()}
	}
	err = cmd.Wait()
	wall := time.Since(start)
	// Stop helpers the process left running so runs stay independent.
	killProcessGroup(cmd)
	result := ProcResult{Wall: wall, Stdout: stdout.String(), Stderr: tail(stderr.String(), 4000), TimedOut: ctx.Err() != nil}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.Code = exitErr.ExitCode()
	default:
		result.Code = 1
	}
	if result.Code < 0 {
		result.Code = 128 + 9
	}
	if cmd.ProcessState != nil {
		result.CPU = cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
		result.RSSMiB = peakRSSMiB(cmd.ProcessState)
	}
	return result
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
