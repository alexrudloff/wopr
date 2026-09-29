package tempfiles

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStart is pid's start time, or ok false when no such process runs.
func processStart(pid int) (string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return "", false
	}
	t := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), true
}
