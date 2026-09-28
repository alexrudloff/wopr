package tui

import "strings"

// UserMessageBlock renders a user message as a card: a heavy left bar in the
// agent color beside a panel holding the message, with a blank row above and
// below the text. The raw ANSI panel is something model output (Markdown)
// cannot produce, so a model can never render text that looks like the
// user's own message.
type UserMessageBlock struct {
	invalidatable
	content string
	inner   *Markdown
}

// NewUserMessageBlock returns a component that renders text as a
// styled user-message block. The text is interpreted as markdown.
func NewUserMessageBlock(text string) *UserMessageBlock {
	return &UserMessageBlock{
		content: text,
		inner:   NewMarkdown(text),
	}
}

func (u *UserMessageBlock) Render(width int) []string {
	width = max(width, 5)
	th := ActiveTheme()
	u.inner.SetDefaultColor(th.Fg("text"))
	bar := AccentColor() + "┃" + SGRFgReset
	panel := th.Bg("backgroundPanel")
	pad := strings.Repeat(" ", userMessagePadX)
	inner := u.inner.Render(max(1, width-1-2*userMessagePadX))
	out := make([]string, 0, len(inner)+2)
	out = append(out, bar+FillBackground("", width-1, panel))
	for _, line := range inner {
		out = append(out, bar+FillBackground(pad+line, width-1, panel))
	}
	return append(out, bar+FillBackground("", width-1, panel))
}

const userMessagePadX = 2

// AccentColor returns the SGR foreground of the agent accent: the bar beside
// user messages and the square closing each turn. It defaults to the
// theme's secondary color; the host may replace it.
var AccentColor = func() string { return ActiveTheme().Fg("secondary") }
