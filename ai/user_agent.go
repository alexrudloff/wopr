package ai

import (
	"runtime"

	"github.com/alexrudloff/wopr/coding/version"
)

// ProductVersion is WOPR's composite release version for outbound identifiers.
// It defaults to version.Version because importing coding would create a cycle.
var ProductVersion = version.Version

// UserAgent reports WOPR's product name and composite version with the
// platform, release and arch: "wopr/<coding.Version> (<platform> <release>;
// <arch>)" (owner decision, delivery/OWNER-DECISIONS.md Q4).
func UserAgent() string {
	return "wopr/" + ProductVersion + " (" + userAgentPlatform(runtime.GOOS) + " " + userAgentRelease() + "; " + userAgentArch(runtime.GOARCH) + ")"
}

// userAgentPlatform maps runtime.GOOS to the vocabulary Node's
// os.platform() uses.
func userAgentPlatform(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// userAgentArch maps runtime.GOARCH to the vocabulary Node's os.arch()
// uses.
func userAgentArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return goarch
	}
}
