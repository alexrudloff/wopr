// Line-level rendering helpers: the per-line reset barrier and image-line
// detection.

package widthx

import "strings"

// SegmentReset is the hard ANSI/OSC barrier appended to non-image lines after
// rendering. It resets all SGR attributes and closes any open OSC 8 hyperlink.
const SegmentReset = "\x1b[0m\x1b]8;;\x07"

const (
	kittyImagePrefix  = "\x1b_G"
	iTerm2ImagePrefix = "\x1b]1337;File="
)

// IsImageLine reports whether a rendered line contains a Kitty or iTerm2
// image graphics sequence. Image lines MUST NOT receive the
// `SegmentReset` suffix because the trailing reset can corrupt the
// image protocol payload.
//
// Checks both the
// fast prefix path (single-row images) and a contains-scan (multi-row
// images that begin with a cursor-up movement before the graphics
// sequence).
//
// Both prefixes start with ESC, so one pass over the line's ESC bytes answers
// both contains-scans; styled rows otherwise pay two full searches each time
// the frame asks (clamp, reset, scrollbar and image-redraw checks).
func IsImageLine(line string) bool {
	for i := 0; ; {
		j := strings.IndexByte(line[i:], 0x1B)
		if j < 0 {
			return false
		}
		rest := line[i+j:]
		if strings.HasPrefix(rest, kittyImagePrefix) || strings.HasPrefix(rest, iTerm2ImagePrefix) {
			return true
		}
		i += j + 1
	}
}

// ApplyLineReset applies terminal normalization and appends the
// `SegmentReset` barrier to a single non-image line. Image lines are
// returned unchanged. This is the per-row body of ApplyLineResets,
// factored out so callers can apply
// it selectively (e.g. an index-aligned reuse cache) without duplicating
// the rule.
func ApplyLineReset(line string) string {
	if IsImageLine(line) {
		return line
	}
	return NormalizeTerminalOutput(line) + SegmentReset
}

// ApplyLineResets returns a new slice where every non-image line has the
// `SegmentReset` barrier appended. Image lines are returned unchanged.
//
// The reset suffix is
// required to prevent SGR / OSC 8 hyperlink state from bleeding between
// adjacent rendered lines.
//
// Returns a new slice; the input slice is not mutated.
func ApplyLineResets(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = ApplyLineReset(line)
	}
	return out
}
