//go:build windows

package ai

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// userAgentRelease returns the Windows version, matching what Node's
// os.release() reports on Windows (major.minor.build).
func userAgentRelease() string {
	info := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
