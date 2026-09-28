package tui

import (
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// AssistantMessageBlock renders ordered text/thinking content and terminal diagnostics for one assistant turn.
type AssistantMessageBlock struct {
	invalidatable
	thinking          string
	text              string
	hidden            bool // whether thinking trace is hidden
	stopReason        string
	errorMessage      string
	hasToolCalls      bool // skip error/abort rendering when tools handle their own
	outputPad         int
	md                *Markdown
	thinkingTransform func(string, int) string
	// content retains untrimmed blocks so later deltas preserve boundary whitespace.
	content  []AssistantSegment
	segments []assistantSegment
}

// AssistantSegment is one text or thinking content block of a complete
// assistant message, in message order.
type AssistantSegment struct {
	Thinking bool
	Text     string
}

type assistantSegment struct {
	thinking bool
	md       *Markdown
}

// NewAssistantMessageBlock creates an empty block. Pass hiddenThinking=true when
// the user has toggled thinking visibility off (Ctrl+T).
func NewAssistantMessageBlock(hiddenThinking bool) *AssistantMessageBlock {
	return &AssistantMessageBlock{
		hidden:    hiddenThinking,
		outputPad: 1,
		md:        NewMarkdown(""),
	}
}

// SetOutputPad changes the horizontal content padding.
func (b *AssistantMessageBlock) SetOutputPad(padding int) {
	b.outputPad = max(0, min(1, padding))
	b.Invalidate()
}

// SetThinkingDelta appends to the current thinking block, or starts one after text.
func (b *AssistantMessageBlock) SetThinkingDelta(delta string) {
	b.appendDelta(true, delta)
}

// SetTextDelta appends to the current text block, or starts one after thinking.
func (b *AssistantMessageBlock) SetTextDelta(delta string) {
	b.appendDelta(false, delta)
}

func (b *AssistantMessageBlock) appendDelta(thinking bool, delta string) {
	if len(b.content) == 0 || b.content[len(b.content)-1].Thinking != thinking {
		b.content = append(b.content, AssistantSegment{Thinking: thinking})
	}
	b.content[len(b.content)-1].Text += delta
	b.SetContent(b.content)
}

// SetContent replaces text/thinking content in message order for streaming or redraw. Blocks are trimmed, empty ones skipped, and consecutive thinking blocks form one run joined by a blank line. An empty text segment preserves an invisible boundary such as a tool call.
func (b *AssistantMessageBlock) SetContent(content []AssistantSegment) {
	b.content = append(b.content[:0], content...)
	b.segments = b.segments[:0]
	var thinking, text []string
	for i := 0; i < len(content); i++ {
		if !content[i].Thinking {
			text = append(text, content[i].Text)
			if trimmed := strings.TrimSpace(content[i].Text); trimmed != "" {
				md := NewMarkdown(trimmed)
				md.Transform = b.md.Transform
				md.TransformState = b.md.TransformState
				b.segments = append(b.segments, assistantSegment{md: md})
			}
			continue
		}
		var run []string
		for ; i < len(content) && content[i].Thinking; i++ {
			if trimmed := strings.TrimSpace(content[i].Text); trimmed != "" {
				run = append(run, trimmed)
			}
		}
		i--
		if len(run) > 0 {
			joined := strings.Join(run, "\n\n")
			md := NewMarkdown(joined)
			md.defaultItalic = true
			md.Transform = b.thinkingTransform
			md.TransformState = b.md.TransformState
			b.segments = append(b.segments, assistantSegment{thinking: true, md: md})
			thinking = append(thinking, joined)
		}
	}
	b.thinking = strings.Join(thinking, "\n\n")
	b.text = strings.Join(text, "")
	b.md.Content = b.text
	b.md.Invalidate()
	b.Invalidate()
}

// SetHiddenThinking controls whether the thinking trace renders as full text
// or as the "Thinking..." indicator.
func (b *AssistantMessageBlock) SetHiddenThinking(hidden bool) {
	b.hidden = hidden
	b.Invalidate()
}

// SetMarkdownTransform installs a display-only rewrite applied to the text
// section at its render width, before markdown parsing. Used for the built-in
// Mermaid transformer.
func (b *AssistantMessageBlock) SetMarkdownTransform(fn func(markdown string, width int) string) {
	b.md.Transform = fn
	b.md.Invalidate()
	for _, seg := range b.segments {
		if !seg.thinking && seg.md != nil {
			seg.md.Transform = fn
			seg.md.Invalidate()
		}
	}
	b.Invalidate()
}

// SetThinkingMarkdownTransform installs the display-only rewrite for visible thinking, separate from the assistant-text transform context. Hidden thinking does not invoke it.
func (b *AssistantMessageBlock) SetThinkingMarkdownTransform(fn func(markdown string, width int) string) {
	b.thinkingTransform = fn
	for _, seg := range b.segments {
		if seg.thinking && seg.md != nil {
			seg.md.Transform = fn
			seg.md.Invalidate()
		}
	}
	b.Invalidate()
}

// SetMarkdownTransformState declares the external state the installed transform
// reads, so a change to it re-renders instead of serving the cached lines.
// Required whenever the transform is not a pure function of (markdown, width).
func (b *AssistantMessageBlock) SetMarkdownTransformState(fn func() string) {
	b.md.TransformState = fn
	b.md.Invalidate()
	for _, seg := range b.segments {
		if seg.md != nil {
			seg.md.TransformState = fn
			seg.md.Invalidate()
		}
	}
	b.Invalidate()
}

// Thinking returns the accumulated thinking content.
func (b *AssistantMessageBlock) Thinking() string { return b.thinking }

// Text returns concatenated untrimmed text blocks, without display transformations.
func (b *AssistantMessageBlock) Text() string { return b.text }

// SetHasToolCalls records that the assistant message contains tool calls.
// When true, the abort/error section is suppressed: tool execution
// components show their own error state.
func (b *AssistantMessageBlock) SetHasToolCalls(v bool) {
	b.hasToolCalls = v
	b.Invalidate()
}

// SetTerminalError records length/error/abort state after partial assistant content. An empty error message renders "Unknown error"; tool calls suppress abort/error but not length diagnostics.
func (b *AssistantMessageBlock) SetTerminalError(stopReason, errorMessage string) {
	b.stopReason = stopReason
	b.errorMessage = errorMessage
	b.Invalidate()
}

// Render implements Component. Every text or thinking part sits one blank line
// below what precedes it, indented under the transcript's text column. It
// returns nil when there is nothing to show.
func (b *AssistantMessageBlock) Render(width int) []string {
	width = max(width, 1)
	indent := strings.Repeat(" ", assistantIndentBase+b.outputPad)
	contentWidth := max(1, width-len(indent)-1)
	var out []string
	for _, seg := range b.segments {
		out = append(out, "")
		if seg.thinking {
			out = append(out, b.renderThinking(seg.md, contentWidth, indent)...)
			continue
		}
		seg.md.SetDefaultColor(ActiveTheme().Fg("markdownText"))
		for _, line := range seg.md.Render(contentWidth) {
			out = append(out, indent+line)
		}
	}
	return append(out, b.renderTerminalError(width, indent)...)
}

// assistantIndentBase plus the output padding (default 1) is the column
// assistant text starts at.
const assistantIndentBase = 2

// thoughtTitle splits a reasoning run that opens with a bold "**Title**"
// paragraph into that title and the remaining body.
func thoughtTitle(text string) (title, body string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "**") {
		return "", trimmed
	}
	end := strings.Index(trimmed[2:], "**")
	if end <= 0 {
		return "", trimmed
	}
	title = trimmed[2 : 2+end]
	rest := trimmed[2+end+2:]
	if rest != "" && !strings.HasPrefix(rest, "\n\n") && strings.TrimSpace(rest) != "" {
		return "", trimmed
	}
	return title, strings.TrimSpace(rest)
}

// renderThinking renders a reasoning run as a "Thought" header in the warning
// color, with the title of a bold-titled run. Hidden thinking shows only the
// header, marked "+"; visible thinking dims the header and follows it with
// the reasoning in muted text.
func (b *AssistantMessageBlock) renderThinking(md *Markdown, contentWidth int, indent string) []string {
	th := ActiveTheme()
	title, body := thoughtTitle(md.Content)
	header := "Thought"
	if title != "" {
		header += ": " + title
	}
	if b.hidden {
		return []string{indent + th.FgText("warning", widthx.TruncateToWidth("+ "+header, contentWidth, "…", false))}
	}
	colors := th.Colors()
	dimmed := ThemeHexFg(BlendHex(colors["background"], colors["warning"], thinkingOpacity))
	out := []string{indent + dimmed + widthx.TruncateToWidth(header, contentWidth, "…", false) + SGRFgReset}
	if body == "" {
		return out
	}
	if title != "" && md.Content != body {
		md = NewMarkdown(body)
		md.defaultItalic = true
	}
	md.SetDefaultColor(th.Fg("textMuted"))
	out = append(out, "")
	for _, line := range md.Render(contentWidth) {
		out = append(out, indent+line)
	}
	return out
}

// thinkingOpacity dims a visible thought's header against the background.
const thinkingOpacity = 0.6

// hasTerminalError reports whether a truncation or error box renders.
func (b *AssistantMessageBlock) hasTerminalError() bool {
	switch {
	case b.stopReason == "length":
		return true
	case b.hasToolCalls:
		return false
	case b.stopReason == "error":
		return true
	}
	// An abort that carries a real cause (a timeout, a provider error) shows
	// it; a plain user abort is reported by the turn footer alone.
	return b.stopReason == "aborted" && b.errorMessage != "" && b.errorMessage != "Request was aborted" && b.errorMessage != "Operation aborted"
}

// renderTerminalError renders a truncation or error as a panel with a heavy
// left bar in the error color and the message in muted text.
func (b *AssistantMessageBlock) renderTerminalError(width int, indent string) []string {
	if !b.hasTerminalError() {
		return nil
	}
	message := b.errorMessage
	switch {
	case b.stopReason == "length":
		message = "Response was truncated before completion."
	case message == "":
		message = "Unknown error"
	}
	return NewErrorPanel("", message).Render(width)
}
