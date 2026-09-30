package codingagent

import (
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// councilBlock is a war council round in the transcript: one line naming
// who answered, expanding to their proposals.
type councilBlock struct {
	tui.BaseComponent
	title, content, hint string
	expanded             bool
}

func (b *councilBlock) SetExpanded(expanded bool) {
	b.expanded = expanded
	b.Invalidate()
}

func (b *councilBlock) SetOutputPad(int) { b.Invalidate() }

func (b *councilBlock) Render(width int) []string {
	th := tui.ActiveTheme()
	line := th.FgText("error", "☢") + " " + th.FgText("text", b.title)
	if !b.expanded && b.hint != "" {
		line += th.FgText("textMuted", " · "+b.hint+" to expand")
	}
	out := []string{"", widthx.TruncateToWidth(line, max(1, width), "…", false)}
	if b.expanded {
		for _, l := range widthx.WrapTextWithAnsi(b.content, max(1, width-5)) {
			out = append(out, th.FgText("textMuted", "   │ ")+l)
		}
	}
	return out
}

// newCouncilBlock titles a council message from its details: "War council:
// 3 proposals (Claude Opus 5.5, GPT-6 Sol, DeepSeek V4 Flash)".
func newCouncilBlock(content string, details any, hint string) *councilBlock {
	d, _ := details.(map[string]any)
	var models []string
	switch v := d["models"].(type) {
	case []string:
		models = v
	case []any:
		for _, m := range v {
			if s, ok := m.(string); ok {
				models = append(models, s)
			}
		}
	}
	title := "War council: no proposals"
	if n := len(models); n > 0 {
		noun := "proposals"
		if n == 1 {
			noun = "proposal"
		}
		title = fmt.Sprintf("War council: %d %s (%s)", n, noun, strings.Join(models, ", "))
	}
	// Each proposal lists its web sources when expanded; the title counts
	// them.
	pages := 0
	switch v := d["sources"].(type) {
	case map[string][]string:
		for _, list := range v {
			pages += len(list)
		}
	case map[string]any:
		for _, list := range v {
			if l, ok := list.([]any); ok {
				pages += len(l)
			}
		}
	}
	if pages > 0 {
		title += fmt.Sprintf(" · %d web sources", pages)
	}
	return &councilBlock{title: title, content: content, hint: hint}
}
