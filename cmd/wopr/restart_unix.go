//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartInto replaces this process with the (upgraded) executable, reopening
// session when it is set.
func restartInto(session string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{exe}
	if session != "" {
		args = append(args, "--session", session)
	}
	return syscall.Exec(exe, args, os.Environ())
}
