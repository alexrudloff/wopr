// Pure-Go ANSI strip + binary sanitizer used by bash output processing.
//
// We keep these in their own file (not pulling in a third-party dep)
// because the rules are tiny and we want predictable behavior.

package tools

import (
	"regexp"
	"strings"
)

// ansiRegex follows chalk's ansi-regex: OSC sequences up to the first string terminator (BEL, ESC \
// or 0x9C), then CSI and related sequences introduced by ESC or the 8-bit
// CSI 0x9B.
var ansiRegex = regexp.MustCompile(
	`(?:\x1b\][\s\S]*?(?:\x07|\x1b\\|\x{9c}))` +
		`|[\x1b\x{9b}][\[\]()#;?]*(?:\d{1,4}(?:[;:]\d{0,4})*)?[\dA-PR-TZcf-nq-uy=><~]`)

// StripANSI removes every ansiRegex match.
// Anything the pattern does not cover (a lone ESC, ESC + letter outside the
// final-byte set) is left for SanitizeBinaryOutput.
func StripANSI(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	// Fast path: ANSI codes require ESC (7-bit) or CSI (8-bit) introducer.
	if !strings.Contains(string(b), "\x1b") && !strings.Contains(string(b), "\u009b") {
		return b
	}
	return ansiRegex.ReplaceAll(b, nil)
}

// SanitizeBinaryOutput drops control characters other than tab, newline and carriage return, and the
// Unicode format characters U+FFF9..U+FFFB (they crash string-width).
// Invalid UTF-8 bytes become U+FFFD.
func SanitizeBinaryOutput(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r <= 0x1f:
		case r >= 0xfff9 && r <= 0xfffb:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
