package tui

import (
	"fmt"
	"regexp"
	"strings"
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
	var out []string
	for _, r := range rows {
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
		for i, part := range wrapText(r.content, contentWidth) {
			left := first
			if i > 0 {
				left = blank
			}
			out = append(out, left+FillBackground(th.FgText("text", part), max(1, width-gutter-1), lineBg))
		}
	}
	return out
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
