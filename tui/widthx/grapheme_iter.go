package widthx

import (
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// graphemeIter walks the extended grapheme clusters of a string with their
// cell widths. It lives on the caller's stack (uniseg.NewGraphemes allocates)
// and skips the Unicode lookups for a printable ASCII byte followed by another
// ASCII byte or the end of the string, which is always a complete one-cell
// cluster.
type graphemeIter struct {
	remaining string
	cluster   string
	width     int
	state     int
}

func newGraphemeIter(s string) graphemeIter {
	return graphemeIter{remaining: s, state: -1}
}

// Next advances to the next cluster and reports whether there was one.
func (g *graphemeIter) Next() bool {
	s := g.remaining
	if s == "" {
		g.cluster = ""
		g.width = 0
		return false
	}
	if c := s[0]; c >= 0x20 && c <= 0x7E && (len(s) == 1 || s[1] < utf8.RuneSelf) {
		g.cluster, g.remaining, g.width, g.state = s[:1], s[1:], 1, -1
		return true
	}
	g.cluster, g.remaining, g.width, g.state = uniseg.FirstGraphemeClusterInString(s, g.state)
	if g.cluster == "\t" {
		g.width = 3
	} else {
		g.width = fixWidth(g.cluster, g.width)
	}
	return true
}

// Str returns the current cluster.
func (g *graphemeIter) Str() string { return g.cluster }

// Width returns the terminal cell width of the current cluster.
func (g *graphemeIter) Width() int { return g.width }
