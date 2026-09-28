package tui

import (
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// PromptPanel draws the editor as a panel: a heavy left bar in the prompt's
// accent color, the text on a backgroundElement panel, a meta line under the
// text, a half-cell lip closing the panel, and a status row below it.
// Autocomplete opens directly above the panel.
//
//	┃                                   panel padding row
//	┃  text rows
//	┃                                   blank row
//	┃  Meta line
//	╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀  lip
//	Status row
type PromptPanel struct {
	// Bar returns the SGR foreground of the left bar and lip corner.
	Bar func() string
	// Meta renders the line under the text at the given width; "" leaves it blank.
	Meta func(width int) string
	// Status renders the row under the panel at the given width; nil omits the row.
	Status func(width int) string
	// StatusShown, when set, omits the status row while it returns false.
	StatusShown func() bool
	// Placeholder is shown in muted text while the buffer is empty.
	Placeholder func() string
	// Cursor draws the cursor cell; nil draws it in reverse video.
	Cursor func(cell string) string
}

// cursorStyle is the panel's cursor style, or nil for reverse video.
func (e *Editor) cursorStyle() func(cell string) string {
	if e.panel == nil {
		return nil
	}
	return e.panel.Cursor
}

// Panel, when set, renders the editor as a PromptPanel instead of the ruled frame.
func (e *Editor) SetPanel(panel *PromptPanel) {
	e.panel = panel
	e.Invalidate()
}

const (
	panelPadX         = 2
	panelAutocomplete = 10
)

func (e *Editor) renderPanel(width int) []string {
	th := ActiveTheme()
	panelBg := th.Bg("backgroundElement")
	bar := th.Fg("border")
	if e.panel.Bar != nil {
		if sgr := e.panel.Bar(); sgr != "" {
			bar = sgr
		}
	}
	if e.IsBashMode() {
		bar = th.Fg("primary")
	}
	innerWidth := max(1, width-1)
	contentWidth := max(1, innerWidth-2*panelPadX)
	layoutWidth := max(1, contentWidth-1) // keep a column for the cursor
	visible, _, _ := e.visibleContent(layoutWidth)
	if e.panel.Placeholder != nil && e.Text() == "" && len(visible) == 1 {
		if text := e.panel.Placeholder(); text != "" {
			text = widthx.TruncateToWidth(text, layoutWidth, "…", false)
			if strings.Contains(visible[0], widthx.CursorMarker) {
				first, rest := splitFirstGrapheme(text)
				cursor := e.panel.Cursor
				if cursor == nil {
					cursor = func(cell string) string { return "\x1b[7m" + cell + "\x1b[27m" }
				}
				visible = []string{widthx.CursorMarker + cursor(th.FgText("textMuted", first)) + th.FgText("textMuted", rest)}
			} else {
				visible = []string{th.FgText("textMuted", text)}
			}
		}
	}

	panelRow := func(content string) string {
		return bar + "┃" + SGRFgReset + FillBackground(content, innerWidth, panelBg)
	}
	pad := strings.Repeat(" ", panelPadX)

	var out []string
	e.renderedAutocompleteHeight = 0
	if len(e.autocompleteItems) > 0 {
		popup := e.renderPanelAutocomplete(width)
		e.renderedAutocompleteHeight = len(popup)
		out = append(out, popup...)
	}
	e.renderedAutocompleteTop = 0
	e.renderedContentTop = len(out) + 1
	e.renderedContentX = 1 + panelPadX

	out = append(out, panelRow(""))
	for _, line := range visible {
		out = append(out, panelRow(pad+line))
	}
	out = append(out, panelRow(""))
	meta := ""
	if e.panel.Meta != nil {
		meta = e.panel.Meta(contentWidth)
	}
	out = append(out, panelRow(pad+meta))
	lip := th.Fg("backgroundElement")
	if lip == SGRFgReset || lip == "" {
		out = append(out, bar+"╹"+SGRFgReset)
	} else {
		out = append(out, bar+"╹"+SGRFgReset+lip+strings.Repeat("▀", innerWidth)+SGRFgReset)
	}
	if e.panel.Status != nil && (e.panel.StatusShown == nil || e.panel.StatusShown()) {
		out = append(out, e.panel.Status(width))
	}
	return out
}

// renderPanelAutocomplete renders up to ten suggestions between heavy side
// bars on the menu background, labels padded to a shared column, the
// selection on the primary color.
func (e *Editor) renderPanelAutocomplete(width int) []string {
	th := ActiveTheme()
	start, end := e.autocompleteVisibleRange()
	if end <= start {
		return nil
	}
	labelWidth := 0
	for _, item := range e.autocompleteItems {
		labelWidth = max(labelWidth, widthx.VisibleWidth(autocompleteLabel(item)))
	}
	labelWidth += 2
	inner := max(1, width-2)
	side := th.Fg("border") + "┃" + SGRFgReset
	menuBg := th.Bg("backgroundMenu")
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := e.autocompleteItems[i]
		label := autocompleteLabel(item)
		label += strings.Repeat(" ", max(0, labelWidth-widthx.VisibleWidth(label)))
		desc := strings.TrimLeft(item.Description, " ")
		var row string
		if i == e.autocompleteCursor {
			fg := th.Fg("selectedListItemText")
			text := widthx.TruncateToWidth(" "+label+desc, inner, "", false)
			row = side + FillBackground(fg+text+SGRFgReset, inner, th.Bg("primary")) + side
		} else {
			text := " " + th.FgText("text", label)
			if desc != "" {
				text += th.FgText("textMuted", desc)
			}
			row = side + FillBackground(widthx.TruncateToWidth(text, inner, "", false), inner, menuBg) + side
		}
		rows = append(rows, row)
	}
	return rows
}

func autocompleteLabel(item AutocompleteItem) string {
	if item.Label != "" {
		return item.Label
	}
	return item.Value
}

// splitFirstGrapheme splits text after its first user-perceived character.
func splitFirstGrapheme(text string) (first, rest string) {
	segments := graphemeSegments(text)
	if len(segments) == 0 {
		return "", ""
	}
	return segments[0].Text, text[len(segments[0].Text):]
}
