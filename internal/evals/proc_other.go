//go:build !darwin && !linux

package evals

import (
	"os"
	"os/exec"
)

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// peakRSSMiB is not measured on this platform.
func peakRSSMiB(*os.ProcessState) float64 { return 0 }
