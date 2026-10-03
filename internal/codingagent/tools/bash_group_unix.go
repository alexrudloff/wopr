//go:build unix

package tools

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so a group-kill
// reaps grandchildren.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the whole process group (negative pid).
func killProcessGroup(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// shellExitCode returns the process exit code. A shell killed by
// a signal has no exit code, so it reports 128 + the signal number rather
// than a value callers could mistake for success.
func shellExitCode(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

// processGroupAlive reports whether any process of group pgid exists.
func processGroupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}

// terminateProcessGroup sends SIGTERM to group pgid.
func terminateProcessGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGTERM) }

// killProcessGroupID sends SIGKILL to group pgid.
func killProcessGroupID(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

// ownProcessGroup is wopr's own process group.
func ownProcessGroup() int { return syscall.Getpgrp() }
