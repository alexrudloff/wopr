// Width-bounded truncation with ellipsis + ANSI/tab awareness.

package widthx

import (
	"strings"
)

// TruncateToWidth truncates text to fit within `maxWidth` visible columns,
// appending `ellipsis` (default "...") when truncation occurs. If `pad` is
// true, the result is padded with trailing spaces to exactly `maxWidth`
// columns.
//
// ANSI escape codes do not count toward width. Tabs expand to 3 columns.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	if text == "" {
		if pad {
			return strings.Repeat(" ", maxWidth)
		}
		return ""
	}

	ellipsisWidth := VisibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		textWidth := VisibleWidth(text)
		if textWidth <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-textWidth)
			}
			return text
		}
		clipped := truncateFragmentToWidth(ellipsis, maxWidth)
		if clipped.width == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		return finalizeTruncatedResult("", 0, clipped.text, clipped.width, maxWidth, pad)
	}

	// Fast ASCII path.
	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-len(text))
			}
			return text
		}
		targetWidth := maxWidth - ellipsisWidth
		return finalizeTruncatedResult(text[:targetWidth], targetWidth, ellipsis, ellipsisWidth, maxWidth, pad)
	}

	targetWidth := maxWidth - ellipsisWidth
	var result strings.Builder
	pendingAnsi := ""
	visibleSoFar := 0
	keptWidth := 0
	keepContiguous := true
	overflowed := false
	// take keeps seg while it fits contiguously and reports whether the
	// text has overflowed maxWidth.
	take := func(seg string, w int) bool {
		if keepContiguous && keptWidth+w <= targetWidth {
			result.WriteString(pendingAnsi)
			result.WriteString(seg)
			keptWidth += w
		} else {
			keepContiguous = false
		}
		pendingAnsi = ""
		visibleSoFar += w
		overflowed = visibleSoFar > maxWidth
		return overflowed
	}

	if !strings.ContainsRune(text, 0x1B) && !strings.ContainsRune(text, '\t') {
		gs := newGraphemeIter(text)
		for gs.Next() {
			seg := gs.Str()
			if take(seg, GraphemeWidth(seg)) {
				break
			}
		}
	} else {
		for i := 0; i < len(text) && !overflowed; {
			if n := ExtractAnsi(text, i); n > 0 {
				pendingAnsi += text[i : i+n]
				i += n
				continue
			}
			if text[i] == '\t' {
				take("\t", 3)
				i++
				continue
			}
			end := i
			for end < len(text) && text[end] != '\t' {
				if ExtractAnsi(text, end) > 0 {
					break
				}
				end++
			}
			gs := newGraphemeIter(text[i:end])
			for gs.Next() {
				seg := gs.Str()
				if take(seg, GraphemeWidth(seg)) {
					break
				}
			}
			i = end
		}
	}

	if !overflowed {
		if pad {
			extra := max(maxWidth-visibleSoFar, 0)
			return text + strings.Repeat(" ", extra)
		}
		return text
	}
	return finalizeTruncatedResult(result.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad)
}

type fragment struct {
	text  string
	width int
}

func truncateFragmentToWidth(text string, maxWidth int) fragment {
	if maxWidth <= 0 || text == "" {
		return fragment{}
	}
	if isPrintableASCII(text) {
		if len(text) > maxWidth {
			return fragment{text: text[:maxWidth], width: maxWidth}
		}
		return fragment{text: text, width: len(text)}
	}
	hasAnsi := strings.ContainsRune(text, 0x1B)
	hasTabs := strings.ContainsRune(text, '\t')
	if !hasAnsi && !hasTabs {
		var b strings.Builder
		width := 0
		gs := newGraphemeIter(text)
		for gs.Next() {
			seg := gs.Str()
			w := GraphemeWidth(seg)
			if width+w > maxWidth {
				break
			}
			b.WriteString(seg)
			width += w
		}
		return fragment{text: b.String(), width: width}
	}
	var b strings.Builder
	width := 0
	pendingAnsi := ""
	for i := 0; i < len(text); {
		if n := ExtractAnsi(text, i); n > 0 {
			pendingAnsi += text[i : i+n]
			i += n
			continue
		}
		if text[i] == '\t' {
			if width+3 > maxWidth {
				break
			}
			if pendingAnsi != "" {
				b.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			b.WriteByte('\t')
			width += 3
			i++
			continue
		}
		end := i
		for end < len(text) && text[end] != '\t' {
			if ExtractAnsi(text, end) > 0 {
				break
			}
			end++
		}
		gs := newGraphemeIter(text[i:end])
		broken := false
		for gs.Next() {
			seg := gs.Str()
			w := GraphemeWidth(seg)
			if width+w > maxWidth {
				broken = true
				break
			}
			if pendingAnsi != "" {
				b.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			b.WriteString(seg)
			width += w
		}
		if broken {
			break
		}
		i = end
	}
	return fragment{text: b.String(), width: width}
}

func finalizeTruncatedResult(prefix string, prefixWidth int, ellipsis string, ellipsisWidth, maxWidth int, pad bool) string {
	const reset = "\x1b[0m"
	hyperlinkClose := activeOsc8Close(prefix)
	visibleWidth := prefixWidth + ellipsisWidth
	var result string
	if ellipsis != "" {
		result = prefix + hyperlinkClose + reset + ellipsis + reset
	} else {
		result = prefix + hyperlinkClose + reset
	}
	if pad {
		extra := max(maxWidth-visibleWidth, 0)
		return result + strings.Repeat(" ", extra)
	}
	return result
}

// activeOsc8Close returns the close sequence (with the opener's terminator)
// when prefix leaves an OSC 8 hyperlink open.
func activeOsc8Close(prefix string) string {
	if !strings.Contains(prefix, "\x1b]8;") {
		return ""
	}
	close := ""
	for i := 0; i < len(prefix); {
		code, n := ExtractAnsiCode(prefix, i)
		if n == 0 {
			i++
			continue
		}
		if strings.HasPrefix(code, "\x1b]8;") {
			term := "\x1b\\"
			if strings.HasSuffix(code, "\x07") {
				term = "\x07"
			}
			body := code[4 : len(code)-len(term)]
			if _, after, ok := strings.Cut(body, ";"); ok {
				if after == "" {
					close = ""
				} else {
					close = "\x1b]8;;" + term
				}
			}
		}
		i += n
	}
	return close
}
