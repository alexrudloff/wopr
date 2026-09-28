package tui

import (
	"strings"
	"unicode"

	"github.com/alexrudloff/wopr/tui/widthx"
)

type graphemeSegment struct {
	Text  string
	Start int
	End   int
	Width int
}

// punctuationChars is the punctuation set (excludes _) used by grapheme-class
// word navigation.
const punctuationChars = "(){}[]<>.,;:'\"!?+-=*/\\|&%^$#@~`"

// grapheme-aware: editor cursor movement, deletion, wrapping, and width must
// operate on user-perceived characters, not individual runes.
func graphemeSegments(s string) []graphemeSegment {
	if s == "" {
		return nil
	}
	var segs []graphemeSegment
	from := 0
	for rest := s; rest != ""; {
		var g string
		g, rest = widthx.FirstGrapheme(rest)
		segs = append(segs, graphemeSegment{Text: g, Start: from, End: from + len(g), Width: widthx.GraphemeWidth(g)})
		from += len(g)
	}
	return segs
}

// grapheme-aware: walk by grapheme boundaries instead of rune boundaries.
func previousGraphemeStart(s string, col int) int {
	if col <= 0 {
		return 0
	}
	segs := graphemeSegments(s[:col])
	if len(segs) == 0 {
		return 0
	}
	return segs[len(segs)-1].Start
}

// grapheme-aware: walk by grapheme boundaries instead of rune boundaries.
func nextGraphemeEnd(s string, col int) int {
	if col >= len(s) {
		return len(s)
	}
	g, _ := widthx.FirstGrapheme(s[col:])
	return col + len(g)
}

func graphemeAt(s string, col int) graphemeSegment {
	for _, seg := range graphemeSegments(s) {
		if col >= seg.Start && col < seg.End {
			return seg
		}
	}
	return graphemeSegment{Start: len(s), End: len(s), Width: 1}
}

func isWhitespaceGrapheme(s string) bool {
	return s != "" && strings.TrimFunc(s, unicode.IsSpace) == ""
}

func isPunctuationGrapheme(s string) bool {
	return s != "" && strings.Trim(s, punctuationChars) == ""
}
