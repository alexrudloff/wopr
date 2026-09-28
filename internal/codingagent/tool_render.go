package codingagent

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// toolBodyRendererForCall derives display-only file metadata from the retained call and result. Write previews and read highlighting use call arguments, not private runtime result details.
func toolBodyRendererForCall(call ai.ToolCall, result agent.AgentToolResult) func(int, bool) []string {
	path, _ := call.Arguments["file_path"].(string)
	if call.Arguments["file_path"] == nil {
		path, _ = call.Arguments["path"].(string)
	}
	switch call.Name {
	case "write":
		content, ok := call.Arguments["content"].(string)
		if !ok {
			return func(width int, _ bool) []string {
				return styleWrapRows("[invalid content arg - expected string]", tui.ActiveTheme().Error, tui.SGRFgReset, width)
			}
		}
		result.Details = &tools.WriteDetails{Path: path, Content: content}
	case "read":
		var details tools.ReadDetails
		switch d := result.Details.(type) {
		case *tools.ReadDetails:
			if d != nil {
				details = *d
			}
		case map[string]any:
			details, _ = decodeAs[tools.ReadDetails](d)
		}
		details.Path = path
		result.Details = &details
	}
	return toolBodyRenderer(call.Name, result, nil)
}

// toolBodyRenderer returns a per-tool body renderer based on the
// AgentToolResult.Details payload, or nil if the tool didn't supply
// typed details. The returned func is wired into the
// ToolExecutionComponent.BodyRenderer slot so Ctrl+O expanding the
// body shows a structured view (unified diff for edit, formatted read/write
// output) instead of raw text.
//
// Renderers return pre-styled visual rows without the `│ ` prefix. The
// component adds the lifecycle background and horizontal padding.
//
// took is the run's duration when the card saw it start, and nil when it
// did not (a result rebuilt from the transcript).
func toolBodyRenderer(toolName string, result agent.AgentToolResult, took *time.Duration) func(width int, expanded bool) []string {
	var elapsed time.Duration
	if took != nil {
		elapsed = *took
	}
	switch toolName {
	case "bash", "powershell":
		// Both shell tools share one renderer.
		return makeShellBodyRenderer(result.Content, result.Details, false, took)
	case "edit":
		if result.IsError {
			return func(width int, _ bool) []string {
				return styleWrapRows(result.Content, tui.ActiveTheme().Error, tui.SGRFgReset, width)
			}
		}
		// Built-in edit tool: render the precomputed display diff
		// (EditToolDetails.diff).
		if d, ok := result.Details.(*tools.EditToolDetails); ok && d != nil {
			return func(width int, _ bool) []string { return renderDiffString(d.Diff, width) }
		}
		// Extension-provided tools named edit can send details as
		// JSON which arrives as map[string]any after detailsToAny. Extract
		// the diff string and render it with line-numbered coloring. This is
		// also the resume path: persisted EditToolDetails reload as a map.
		if diffStr := extractDiffString(result.Details); diffStr != "" {
			return func(width int, _ bool) []string { return renderDiffString(diffStr, width) }
		}
		return nil
	case "grep", "find", "ls":
		return makeListBodyRenderer(toolName, result.Content, result.Details)
	case "write":
		d, ok := result.Details.(*tools.WriteDetails)
		if !ok || d == nil {
			return nil
		}
		return func(width int, expanded bool) []string {
			out := renderWriteContent(d, width, expanded)
			if result.IsError && result.Content != "" {
				if len(out) > 0 {
					out = append(out, "")
				}
				out = append(out, styleWrapRows(result.Content, tui.ActiveTheme().Error, tui.SGRFgReset, width)...)
			}
			return out
		}
	case "read":
		d, ok := result.Details.(*tools.ReadDetails)
		if !ok || d == nil {
			return nil
		}
		return func(width int, expanded bool) []string {
			if result.IsError {
				return renderReadError(result.Content, width, expanded)
			}
			return renderReadLines(d, result.Content, width, expanded)
		}
	}

	// Extension-provided preview: when the tool result carries a Preview
	// string, use it as the collapsed view (instead of the generic
	// tail-of-output preview). The full Content is shown on Ctrl+O expand.
	// This gives extensions control over collapsed rendering without
	// implementing a full BodyRenderer.
	if result.Preview != "" {
		return makePreviewBodyRenderer(result.Content, result.Preview, elapsed)
	}

	return nil
}

// extractDiffString retrieves a "diff" string from generic details
// returned by extension-provided tools. Extensions serialize details as
// JSON which arrives in Go as map[string]any after detailsToAny.
func extractDiffString(details any) string {
	if d, ok := details.(*tools.EditToolDetails); ok && d != nil {
		return d.Diff
	}
	m, _ := details.(map[string]any)
	s, _ := m["diff"].(string)
	return s
}

// renderDiffString renders a pre-generated diff string (from
// generateDiffString) as unified diff rows.
func renderDiffString(diffText string, width int) []string {
	return tui.RenderDiffRows(diffText, width)
}

// renderWriteContent shows written content: ten logical lines while collapsed, every logical line while expanded, and
// Text-style wrapping for every visual row.
func renderWriteContent(d *tools.WriteDetails, width int, expanded bool) []string {
	body := strings.TrimRight(d.Content, "\n")
	if body == "" {
		return nil
	}
	// Tabs expand to three spaces before display.
	body = strings.ReplaceAll(body, "\t", "   ")
	lines := strings.Split(body, "\n")

	// Syntax-highlight when we have a recognized language.
	var styled []string
	if lang := tui.LanguageFromPath(d.Path); lang != "" {
		hl := tui.HighlightCode(body, lang)
		if len(hl) == len(lines) {
			styled = hl
		}
	}

	const writePreviewLines = 10
	totalLines := len(lines)
	maxLines := totalLines
	if !expanded && totalLines > writePreviewLines {
		maxLines = writePreviewLines
	}

	var out []string
	for i := 0; i < maxLines; i++ {
		if styled != nil {
			out = append(out, widthx.WrapTextWithAnsi(styled[i], max(1, width))...)
		} else {
			out = append(out, toolOutputWrap(lines[i], width)...)
		}
	}
	if maxLines < totalLines {
		hint := fmt.Sprintf("... (%d more lines, %d total, ctrl+o to expand)", totalLines-maxLines, totalLines)
		out = append(out, mutedWrap(hint, width)...)
	}
	return out
}

// renderReadLines formats a read result. Collapsed successful reads
// have no body. Expanded reads show every source line without an added gutter
// or summary header, wrap via Text semantics, and append the
// truncation warning when the tool supplied truncation details.
func renderReadLines(d *tools.ReadDetails, content string, width int, expanded bool) []string {
	if !expanded {
		return nil
	}
	content = strings.ReplaceAll(content, "\t", "   ")
	trimmed := strings.TrimRight(content, "\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}

	var styled []string
	if lang := tui.LanguageFromPath(d.Path); lang != "" && len(lines) > 0 {
		hl := tui.HighlightCode(strings.Join(lines, "\n"), lang)
		if len(hl) == len(lines) {
			styled = hl
		}
	}

	out := make([]string, 0, len(lines)+1)
	for i, line := range lines {
		if styled != nil {
			out = append(out, widthx.WrapTextWithAnsi(styled[i], max(1, width))...)
		} else {
			out = append(out, toolOutputWrap(line, width)...)
		}
	}
	if d.Truncation != nil {
		if warning := tools.FormatTruncationWarning(*d.Truncation); warning != "" {
			out = append(out, warningWrap(warning, width)...)
		}
	}
	return out
}

func renderReadError(content string, width int, expanded bool) []string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	shown := len(lines)
	if !expanded {
		shown = min(shown, 10)
	}
	var out []string
	for _, line := range lines[:shown] {
		out = append(out, toolOutputWrap(line, width)...)
	}
	if shown < len(lines) {
		out = append(out, mutedWrap(fmt.Sprintf("... (%d more lines, ctrl+o to expand)", len(lines)-shown), width)...)
	}
	return out
}

// mutedFallback is gray #808080, for themes without a muted color.
const mutedFallback = "\033[38;2;128;128;128m"

// The wrap helpers preserve complete tool content across rows.
func mutedWrap(s string, width int) []string {
	return styleWrapRows(s, cmp.Or(tui.ActiveTheme().Muted, mutedFallback), tui.SGRFgReset, width)
}

func warningWrap(s string, width int) []string {
	return styleWrapRows(s, cmp.Or(tui.ActiveTheme().Warning, "\033[33m"), tui.SGRFgReset, width)
}

func styleWrapRows(s, open, close string, width int) []string {
	wrapped := widthx.WrapTextWithAnsi(s, max(1, width))
	out := make([]string, len(wrapped))
	for i, line := range wrapped {
		out[i] = open + line + close
	}
	return out
}

// mutedTrunc applies the theme muted color (gray #808080) and truncates.
func mutedTrunc(s string, width int) string {
	return cmp.Or(tui.ActiveTheme().Muted, mutedFallback) + truncToWidth(s, width) + tui.SGRFgReset
}

// toolOutputWrap styles a line with the tool-output color and wraps it
// to `width`, returning one or more lines (word-wrapped, ANSI-aware).
func toolOutputWrap(s string, width int) []string {
	c := cmp.Or(tui.ActiveTheme().ToolOutput, mutedFallback)
	// Replace tabs before wrapping: tab stops differ across terminals.
	s = strings.ReplaceAll(s, "\t", "   ")
	lines := tui.WrapText(s, width)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = c + l + tui.SGRFgReset
	}
	return out
}

func truncToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if widthx.VisibleWidth(s) <= width {
		return s
	}
	// Plain-text ellipsis truncation measured by widthx (no SGR resets
	// inserted, so the caller's surrounding style also covers the ellipsis).
	return widthx.SliceByColumn(s, 0, width-1, true) + "\u2026"
}

// makePreviewBodyRenderer produces a body renderer that uses an
// extension-provided preview string for the collapsed view and the full
// content for the expanded view. This gives extensions the same
// collapsed/expanded UX as built-in tools without implementing a full
// BodyRenderer. The preview is shown verbatim (one line
// per \n-separated segment); Ctrl+O reveals the full output.
func makePreviewBodyRenderer(content, preview string, elapsed time.Duration) func(width int, expanded bool) []string {
	return func(width int, expanded bool) []string {
		var out []string
		if expanded {
			output := strings.TrimRight(content, "\n")
			if output != "" {
				for line := range strings.SplitSeq(output, "\n") {
					out = append(out, toolOutputWrap(line, width)...)
				}
			}
		} else {
			// Show the extension-provided preview.
			for line := range strings.SplitSeq(strings.TrimRight(preview, "\n"), "\n") {
				out = append(out, toolOutputWrap(line, width)...)
			}
			// Add expand hint if the full content is longer than the preview.
			contentLines := strings.Count(content, "\n")
			previewLines := strings.Count(preview, "\n")
			if contentLines > previewLines {
				hint := fmt.Sprintf("... (%d more lines, ctrl+o to expand)", contentLines-previewLines)
				out = append(out, mutedTrunc(hint, width))
			}
		}

		if elapsed > 0 {
			label := fmt.Sprintf("Took %.1fs", elapsed.Seconds())
			out = append(out, "")
			out = append(out, mutedTrunc(label, width))
		}

		return out
	}
}
