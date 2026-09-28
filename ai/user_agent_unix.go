//go:build unix

package ai

import "golang.org/x/sys/unix"

// userAgentRelease returns the kernel release, matching what Node's
// os.release() reports on unix (uname's release field).
func userAgentRelease() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return ""
	}
	return unix.ByteSliceToString(name.Release[:])
}
