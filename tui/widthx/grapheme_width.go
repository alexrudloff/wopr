package widthx

import (
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// FirstGrapheme returns the first extended grapheme cluster of s (UAX #29)
// and the remainder.
func FirstGrapheme(s string) (cluster, rest string) {
	cluster, rest, _, _ = uniseg.FirstGraphemeClusterInString(s, -1)
	return cluster, rest
}

// GraphemeWidth returns the terminal cell width of one grapheme cluster
// produced by FirstGrapheme. A tab counts as 3 cells, like VisibleWidth.
func GraphemeWidth(seg string) int {
	if seg == "\t" {
		return 3
	}
	return fixWidth(seg, uniseg.StringWidth(seg))
}

// fixWidth widens a one-cell cluster carrying VS16 (emoji presentation, e.g.
// the keycap "1️⃣") to the two cells terminals paint it with.
func fixWidth(seg string, width int) int {
	if width == 1 && len(seg) > 3 && strings.ContainsRune(seg, 0xFE0F) {
		return 2
	}
	return width
}

// isCJKBreak reports whether segment contains a Han, Hiragana, Katakana,
// Hangul or Bopomofo code point, CJK punctuation or a fullwidth form: text
// that may wrap between any two characters.
func isCJKBreak(segment string) bool {
	for _, r := range segment {
		if (r >= 0x3000 && r <= 0x303F) || r == 0x30FC || (r >= 0xFF00 && r <= 0xFFEF) ||
			unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo) {
			return true
		}
	}
	return false
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func trimRightSpace(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }
