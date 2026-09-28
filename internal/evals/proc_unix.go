//go:build darwin || linux

package evals

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// peakRSSMiB reads ru_maxrss, which darwin reports in bytes and linux in KiB.
func peakRSSMiB(state *os.ProcessState) float64 {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return float64(usage.Maxrss) / (1 << 20)
	}
	return float64(usage.Maxrss) / (1 << 10)
}
