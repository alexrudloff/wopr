package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// DimLine darkens a line as if covered by black at the given opacity: every
// truecolor foreground and background is scaled toward black, and text in
// the default foreground is drawn in defaultFgHex scaled the same way.
// Palette colors are left as they are.
func DimLine(line string, opacity float64, defaultFgHex string) string {
	if opacity <= 0 || widthx.IsImageLine(line) {
		return line
	}
	keep := 1 - opacity
	defaultFg := ""
	if r, g, b, ok := parseHexRGB(defaultFgHex); ok {
		defaultFg = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", scaleChannel(r, keep), scaleChannel(g, keep), scaleChannel(b, keep))
	}
	var out strings.Builder
	out.Grow(len(line) + 32)
	out.WriteString(defaultFg)
	for i := 0; i < len(line); {
		if line[i] != 0x1b || i+1 >= len(line) || line[i+1] != '[' {
			next := strings.IndexByte(line[i+1:], 0x1b)
			if next < 0 {
				out.WriteString(line[i:])
				break
			}
			out.WriteString(line[i : i+1+next])
			i += 1 + next
			continue
		}
		end := i + 2
		for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
			end++
		}
		if end >= len(line) {
			out.WriteString(line[i:])
			break
		}
		if line[end] != 'm' {
			out.WriteString(line[i : end+1])
			i = end + 1
			continue
		}
		rewritten, resetsFg := dimSGR(line[i+2:end], keep)
		out.WriteString("\x1b[" + rewritten + "m")
		if resetsFg {
			out.WriteString(defaultFg)
		}
		i = end + 1
	}
	return out.String()
}

func scaleChannel(c int, keep float64) int { return int(float64(c)*keep + 0.5) }

// dimSGR scales the truecolor arguments of one SGR parameter list and reports
// whether it resets the foreground.
func dimSGR(params string, keep float64) (string, bool) {
	if params == "" {
		return "0", true
	}
	fields := strings.Split(params, ";")
	resets := false
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "0", "39":
			resets = true
		case "38", "48", "58":
			if i+1 >= len(fields) {
				continue
			}
			switch fields[i+1] {
			case "5":
				i += 2
			case "2":
				for j := i + 2; j <= i+4 && j < len(fields); j++ {
					if v, err := strconv.Atoi(fields[j]); err == nil {
						fields[j] = strconv.Itoa(scaleChannel(v, keep))
					}
				}
				i += 4
			}
		}
	}
	return strings.Join(fields, ";"), resets
}
