package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// TurnFooter closes a finished turn: "▣  Model · 1m 58s", with the square in
// the accent color, or muted and "· interrupted" for an aborted turn.
type TurnFooter struct {
	invalidatable
	Model       string
	Duration    time.Duration
	Interrupted bool
}

func (f *TurnFooter) Render(width int) []string {
	th := ActiveTheme()
	square := AccentColor() + "▣" + SGRFgReset
	if f.Interrupted {
		square = th.FgText("textMuted", "▣")
	}
	line := strings.Repeat(" ", assistantIndentBase+1) + square + "  " + th.FgText("text", f.Model)
	switch {
	case f.Interrupted:
		line += th.FgText("textMuted", " · interrupted")
	case f.Duration > 0:
		line += th.FgText("textMuted", " · "+FormatTurnDuration(f.Duration))
	}
	return []string{"", widthx.TruncateToWidth(line, max(1, width), "…", false)}
}

// FormatTurnDuration renders 850ms, 4.2s, 1m 58s, 2h 5m, or 1d 3h.
func FormatTurnDuration(d time.Duration) string {
	ms := d.Milliseconds()
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
