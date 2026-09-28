package tui

import (
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// FillBackground paints bg (an SGR background escape) under every cell of line
// that has no background of its own, and pads the line to width with it. A
// full reset or a default-background SGR inside the line re-applies bg, so
// components that close their own colors never punch holes through the
// screen background. Image lines and an empty bg are returned unchanged.
func FillBackground(line string, width int, bg string) string {
	if bg == "" || widthx.IsImageLine(line) {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + len(bg)*2 + width)
	b.WriteString(bg)
	for i := 0; i < len(line); {
		if line[i] != 0x1b || i+1 >= len(line) || line[i+1] != '[' {
			next := strings.IndexByte(line[i+1:], 0x1b)
			if next < 0 {
				b.WriteString(line[i:])
				break
			}
			b.WriteString(line[i : i+1+next])
			i += 1 + next
			continue
		}
		end := i + 2
		for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
			end++
		}
		if end >= len(line) {
			b.WriteString(line[i:])
			break
		}
		b.WriteString(line[i : end+1])
		if line[end] == 'm' && sgrClearsBackground(line[i+2:end]) {
			b.WriteString(bg)
		}
		i = end + 1
	}
	if pad := width - widthx.VisibleWidth(line); pad > 0 {
		b.WriteString(bg)
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(SGRBgReset)
	return b.String()
}

// sgrClearsBackground reports whether an SGR parameter string resets the
// background: an empty list, a 0 parameter, or 49. Extended color arguments
// (38/48/58 with 5;n or 2;r;g;b) are skipped so a zero channel is not read as
// a reset.
func sgrClearsBackground(params string) bool {
	if params == "" {
		return true
	}
	fields := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "0", "00", "49":
			return true
		case "38", "48", "58":
			if i+1 < len(fields) {
				switch fields[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		}
	}
	return false
}
