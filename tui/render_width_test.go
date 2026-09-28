package tui

// One rendered line must occupy exactly one terminal row: no component may emit
// a row wider than the width it was given (the overflow guard crashes on it).

import (
	"io"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/tui/widthx"
)

var widthCorpus = []string{
	"👨‍👩‍👧‍👦🏳️‍🌈🇺🇸1️⃣⚠️👍🏽 日本語のテキストと한국어 ｶﾀｶﾅ Z̴̢̛̗a̷͚l क्ष \u200b\u00ad",
	"\x1b[1;31mbold red\x1b[0m \x1b]8;;https://example.com/long/path\x1b\\link text\x1b]8;;\x1b\\ tail\ttab",
	"See https://example.com/" + strings.Repeat("segment/", 20) + "end and " + strings.Repeat("x", 300),
	"```go\nfunc main() { fmt.Println(\"a line of code well past any terminal width 👨‍👩‍👧‍👦\") }\n```",
	"| 名前 | column two | ✓ |\n| --- | --- | --- |\n| " + strings.Repeat("y", 120) + " | テキスト | ⚠️ |",
	"- " + strings.Repeat("nested ", 30) + "\n  1. " + strings.Repeat("deeper ", 30) + "\n> quote $\\infty$",
	"The value $x^2 + y^2$ costs $5.\n\n$$\\begin{pmatrix} a & b \\\\ c & d \\end{pmatrix}$$\n\n\\[ x = \\frac{-b}{2a} \\]",
	"```mermaid\ngraph TD\n  A[Start 開始] --> B{Decision with a very long label}\n  B -->|yes| C[" + strings.Repeat("node", 30) + "]\n```",
}

func TestRenderedRowsNeverExceedWidth(t *testing.T) {
	all := strings.Join(widthCorpus, "\n\n")
	components := map[string]func(int) []string{
		"Markdown": func(w int) []string { return NewMarkdown(all).Render(w) },
		"Text":     func(w int) []string { return NewText(all).Render(w) },
		"Editor": func(w int) []string {
			e := NewEditor()
			e.SetText(all)
			return e.Render(w)
		},
		"FilterableList": func(w int) []string {
			return NewFilterableList("選択 👨‍👩‍👧‍👦", widthCorpus).Render(w)
		},
		"ToolExecution": func(w int) []string {
			c := NewToolExecutionComponent("bash 👨‍👩‍👧‍👦", all)
			c.SetResult(all, true, 0)
			c.SetExpanded(true)
			return c.Render(w)
		},
	}
	for name, render := range components {
		for _, w := range []int{1, 2, 20, 80} {
			// The tool card's fixed gutter needs more than a couple of columns.
			if name == "ToolExecution" && w < 20 {
				continue
			}
			for i, row := range render(w) {
				// At width 1 a single wide grapheme (2 cells) cannot be split further.
				if vw := widthx.VisibleWidth(row); vw > max(w, 2) {
					t.Errorf("%s width=%d row %d: visible width %d: %q", name, w, i, vw, row)
				}
			}
		}
	}
}

type linesComponent struct{ lines []string }

func (c *linesComponent) Render(int) []string { return c.lines }
func (*linesComponent) Invalidate()           {}

func TestOverlayCompositeNeverExceedsWidth(t *testing.T) {
	overlay := strings.Split(strings.Join(widthCorpus, "\n"), "\n")
	for _, tw := range []int{20, 80} {
		var bg []string
		for _, l := range overlay {
			bg = append(bg, widthx.TruncateToWidth(l, tw, "", false))
		}
		for _, col := range []int{0, tw / 2, tw - 1} {
			ui := NewWithOutput(io.Discard, tw, len(bg), Options{})
			ui.OpenOverlay(&linesComponent{lines: overlay},
				OverlayOptions{Width: 7, Anchor: "top-left", Margin: OverlayMargin{Top: 1, Left: col}})
			for i, line := range ui.composeOverlayLines(bg, tw, len(bg)) {
				if vw := widthx.VisibleWidth(line); vw > tw {
					t.Errorf("tw=%d col=%d row %d: visible width %d", tw, col, i, vw)
				}
			}
		}
	}
}
