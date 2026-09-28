package tui

import "strings"

// ErrorPanel is a failure shown in the transcript: a panel with an
// error-colored left bar, a bold title, and optional muted detail.
type ErrorPanel struct {
	Title  string
	Detail string
}

// NewErrorPanel returns an ErrorPanel for title and detail.
func NewErrorPanel(title, detail string) *ErrorPanel {
	return &ErrorPanel{Title: title, Detail: detail}
}

// Invalidate is a no-op; the panel renders from its fields every time.
func (p *ErrorPanel) Invalidate() {}

// Render draws the panel at width.
func (p *ErrorPanel) Render(width int) []string {
	th := ActiveTheme()
	panel := th.Bg("backgroundPanel")
	bar := th.Fg("error") + "┃" + SGRFgReset
	const pad = "  "
	inner := max(1, width-1-2*len(pad))
	row := func(text string) string { return bar + FillBackground(pad+text, width-1, panel) }
	out := []string{"", row("")}
	for _, line := range wrapText(strings.TrimSpace(p.Title), inner) {
		if line == "" {
			continue
		}
		out = append(out, row("\x1b[1m"+th.FgText("error", line)+SGRBoldDimReset))
	}
	if detail := strings.TrimSpace(p.Detail); detail != "" {
		for _, line := range wrapText(detail, inner) {
			out = append(out, row(th.FgText("textMuted", line)))
		}
	}
	return append(out, row(""))
}
