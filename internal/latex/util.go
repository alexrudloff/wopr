package latex

import (
	"slices"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// visibleWidth delegates to the canonical widthx.VisibleWidth, so LaTeX layout and the TUI renderer agree on
// every row's width.
func visibleWidth(s string) int {
	return widthx.VisibleWidth(s)
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// sliceRunesFrom drops the first n runes of s (n leading layout spaces), guarding
// short lines the way JS String.slice(n) yields "" past the end.
func sliceRunesFrom(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if n >= len(r) {
		return ""
	}
	return string(r[n:])
}

func indexOfRune(src []rune, sub string, from int) int {
	subR := []rune(sub)
	if len(subR) == 0 {
		return from
	}
	from = max(from, 0)
	for i := from; i+len(subR) <= len(src); i++ {
		if runesHavePrefix(src[i:], subR) {
			return i
		}
	}
	return -1
}

func runesHavePrefix(src, prefix []rune) bool {
	return len(src) >= len(prefix) && slices.Equal(src[:len(prefix)], prefix)
}
