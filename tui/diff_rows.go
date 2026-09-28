package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// diffLineRe matches a display diff line: "+123 content", "-123 content", or
// " 123 content".
var diffLineRe = regexp.MustCompile(`^([+\- ])([ ]*\d*)[ ](.*)$`)

type diffLineParsed struct {
	prefix  string
	lineNum string
	content string
}

// parseDiffLine extracts the sign, line number, and content of a diff line,
// or returns nil when the line is not a diff row.
func parseDiffLine(line string) *diffLineParsed {
	m := diffLineRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	return &diffLineParsed{prefix: m[1], lineNum: m[2], content: m[3]}
}

// replaceTabs replaces each tab with three spaces.
func replaceTabs(text string) string {
	return strings.ReplaceAll(text, "\t", "   ")
}

// RenderDiffRows renders a display diff ("+12 added", "-12 removed",
// " 12 context" lines) as unified rows: a line-number gutter with a " +" or
// " -" sign, then the line on the added, removed, or context background.
// Long lines wrap under the gutter; lines that are not diff rows (hunk
// separators) are skipped.
func RenderDiffRows(diffText string, width int) []string {
	type row struct {
		sign    string
		number  string
		content string
	}
	var rows []row
	digits := 1
	for line := range strings.SplitSeq(diffText, "\n") {
		parsed := parseDiffLine(line)
		if parsed == nil {
			continue
		}
		number := strings.TrimSpace(parsed.lineNum)
		digits = max(digits, len(number))
		rows = append(rows, row{sign: parsed.prefix, number: number, content: replaceTabs(parsed.content)})
	}
	if len(rows) == 0 {
		return nil
	}
	th := ActiveTheme()
	gutter := max(3, digits+2) + 2
	contentWidth := max(1, width-gutter-1)
	spans := pairChangedSpans(rows, func(r row) (string, string) { return r.sign, r.content })
	emphasis := map[string]string{
		"+": th.blendBg("diffAddedBg", "diffHighlightAdded", wordEmphasisShare),
		"-": th.blendBg("diffRemovedBg", "diffHighlightRemoved", wordEmphasisShare),
	}
	var out []string
	for idx, r := range rows {
		gutterBg, lineBg, sign, signToken := th.Bg("diffContextBg"), th.Bg("diffContextBg"), "  ", ""
		switch r.sign {
		case "+":
			gutterBg, lineBg, sign, signToken = th.Bg("diffAddedLineNumberBg"), th.Bg("diffAddedBg"), " +", "diffHighlightAdded"
		case "-":
			gutterBg, lineBg, sign, signToken = th.Bg("diffRemovedLineNumberBg"), th.Bg("diffRemovedBg"), " -", "diffHighlightRemoved"
		}
		numberCell := fmt.Sprintf("%*s", gutter-3, r.number)
		signCell := sign
		if signToken != "" {
			signCell = th.FgText(signToken, sign)
		}
		first := FillBackground(th.FgText("diffLineNumber", numberCell)+signCell+" ", gutter+1, gutterBg)
		blank := FillBackground("", gutter+1, gutterBg)
		content := r.content
		if span, ok := spans[idx]; ok && emphasis[r.sign] != "" {
			runes := []rune(content)
			content = string(runes[:span[0]]) + emphasis[r.sign] + string(runes[span[0]:span[1]]) + SGRBgReset + string(runes[span[1]:])
		}
		for i, part := range wrapText(content, contentWidth) {
			left := first
			if i > 0 {
				left = blank
			}
			out = append(out, left+FillBackground(th.FgText("text", part), max(1, width-gutter-1), lineBg))
		}
	}
	return out
}

// wordEmphasisShare is how far a changed span's background moves from the
// line's background toward the sign color.
const wordEmphasisShare = 0.4

// wordEmphasisMaxShare is the largest share of a line that may be emphasized;
// a line changed beyond it is shown as a whole-line change only.
const wordEmphasisMaxShare = 0.7

// pairChangedSpans pairs each run of removed lines with the run of added
// lines right after it, line by line, and returns for each paired row the
// rune range [start, end) that changed. Rows without a pair, identical rows,
// and rows changed almost entirely get no span.
func pairChangedSpans[R any](rows []R, line func(R) (sign, content string)) map[int][2]int {
	spans := map[int][2]int{}
	for i := 0; i < len(rows); {
		if sign, _ := line(rows[i]); sign != "-" {
			i++
			continue
		}
		removedStart := i
		for i < len(rows) {
			if sign, _ := line(rows[i]); sign != "-" {
				break
			}
			i++
		}
		addedStart := i
		for i < len(rows) {
			if sign, _ := line(rows[i]); sign != "+" {
				break
			}
			i++
		}
		for k := 0; removedStart+k < addedStart && addedStart+k < i; k++ {
			_, oldText := line(rows[removedStart+k])
			_, newText := line(rows[addedStart+k])
			oldSpan, newSpan, ok := changedSpan([]rune(oldText), []rune(newText))
			if !ok {
				continue
			}
			if oldSpan[1] > oldSpan[0] {
				spans[removedStart+k] = oldSpan
			}
			if newSpan[1] > newSpan[0] {
				spans[addedStart+k] = newSpan
			}
		}
	}
	return spans
}

// changedSpan finds the differing middle of two lines after their common
// prefix and suffix, widened to whole words. ok is false when the lines are
// equal or the change covers most of either line.
func changedSpan(a, b []rune) (spanA, spanB [2]int, ok bool) {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	if prefix == len(a) && prefix == len(b) {
		return spanA, spanB, false
	}
	// Widen to word boundaries so a word is emphasized whole.
	for prefix > 0 && isWordRune(a[prefix-1]) && (prefix < len(a) && isWordRune(a[prefix]) || prefix < len(b) && isWordRune(b[prefix])) {
		prefix--
	}
	for suffix > 0 && isWordRune(a[len(a)-suffix]) && (len(a)-suffix-1 >= prefix && isWordRune(a[len(a)-suffix-1]) || len(b)-suffix-1 >= prefix && isWordRune(b[len(b)-suffix-1])) {
		suffix--
	}
	spanA, spanB = [2]int{prefix, len(a) - suffix}, [2]int{prefix, len(b) - suffix}
	if tooMuch(a, spanA) || tooMuch(b, spanB) {
		return spanA, spanB, false
	}
	return spanA, spanB, true
}

// tooMuch reports whether span covers more than wordEmphasisMaxShare of the
// line's non-space text.
func tooMuch(line []rune, span [2]int) bool {
	text, changed := 0, 0
	for i, r := range line {
		if unicode.IsSpace(r) {
			continue
		}
		text++
		if i >= span[0] && i < span[1] {
			changed++
		}
	}
	return text > 0 && float64(changed) > wordEmphasisMaxShare*float64(text)
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// blendBg returns a background escape share of the way from token from to
// token to, or "" when either is not a hex color.
func (t *Theme) blendBg(from, to string, share float64) string {
	fr, fg, fb, ok1 := parseHex(t.colors[from].Text)
	tr, tg, tb, ok2 := parseHex(t.colors[to].Text)
	if !ok1 || !ok2 || t.colors[from].IsIndex || t.colors[to].IsIndex {
		return ""
	}
	mix := func(a, b int) int { return a + int(float64(b-a)*share+0.5) }
	hex := "#" + hex2(mix(fr, tr)) + hex2(mix(fg, tg)) + hex2(mix(fb, tb))
	return hexColorANSI(hex, t.ColorMode(), layerBg)
}

func hex2(v int) string {
	s := strconv.FormatInt(int64(v), 16)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

// DiffStats counts added and removed lines in a display diff.
func DiffStats(diffText string) (added, removed int) {
	for line := range strings.SplitSeq(diffText, "\n") {
		if parsed := parseDiffLine(line); parsed != nil {
			switch parsed.prefix {
			case "+":
				added++
			case "-":
				removed++
			}
		}
	}
	return added, removed
}
