package tui

// slot_editor.go: a bordered multi-line editor shown in the editor slot.
//
// Layout:
//
//   DynamicBorder
//   Spacer(1)
//   Text(theme.fg("accent", title))   : indented 1
//   Spacer(1)
//   Editor                            : multi-line, Enter submits, Shift+Enter/Ctrl+J newline
//   Spacer(1)
//   Text(hint)                        : indented 1
//   Spacer(1)
//   DynamicBorder
//
// Used for /tree's custom summarization instructions and the /bug
// description.

// SlotEditorComponent wraps a fresh Editor in borders, a title, and a hint.
type SlotEditorComponent struct {
	invalidatable
	editor      *Editor
	title       string
	description string
	done        bool
	cancel      bool
	value       string
}

// NewSlotEditorComponent creates the editor-slot editor.
// prefill pre-populates the editor content.
func NewSlotEditorComponent(title, prefill string) *SlotEditorComponent {
	ed := NewEditor()
	// A small max visible lines count, since it
	// sits in the editor slot alongside chrome.
	ed.SetMaxVisibleLines(5)
	ed.Focused = true
	if prefill != "" {
		ed.SetText(prefill)
	}

	c := &SlotEditorComponent{
		editor: ed,
		title:  title,
	}

	// Wire Enter → submit via Editor.OnSubmit.
	ed.OnSubmit = func(text string) {
		c.value = text
		c.done = true
	}

	return c
}

// SetDescription sets optional explanatory text shown between the title and
// the editor.
func (c *SlotEditorComponent) SetDescription(description string) {
	c.description = description
	c.Invalidate()
}

// Done reports whether the user submitted or cancelled.
func (c *SlotEditorComponent) Done() bool { return c.done }

// Cancelled reports whether the user pressed Esc/Ctrl+C.
func (c *SlotEditorComponent) Cancelled() bool { return c.cancel }

// Value returns the submitted text (empty if cancelled).
func (c *SlotEditorComponent) Value() string {
	if c.cancel {
		return ""
	}
	return c.value
}

// HandleInput processes key events. Esc/Ctrl+C cancels; everything
// else is forwarded to the underlying Editor (which calls OnSubmit
// on Enter, handles Shift+Enter/Ctrl+J as newline).
func (c *SlotEditorComponent) HandleInput(data string) {
	if c.done {
		return
	}
	kb := Keybindings()
	if kb.Matches(data, KBSelectCancel) {
		c.cancel = true
		c.done = true
		c.Invalidate()
		return
	}
	c.editor.HandleInput(data)
	c.Invalidate()
}

// Render draws the editor layout:
//
//	DynamicBorder + Spacer + accent(title) indented 1 + Spacer +
//	Editor (with its own internal dashed borders) + Spacer +
//	hint line indented 1 + Spacer + DynamicBorder.
func (c *SlotEditorComponent) Render(width int) []string {
	t := ActiveTheme()

	var lines []string
	lines = append(lines, NewPaddedText(t.Accent+c.title+t.Reset, 1, 0, nil).Render(width)...)
	lines = append(lines, dialogDescriptionLines(c.description, width)...)
	// No spacer here: the Editor's Render starts with a blank line
	// ("one blank row above the top border") that provides the gap.
	lines = append(lines, c.editor.Render(width)...)
	lines = append(lines, "")

	// Hint line: tui.select.confirm → "enter",
	// tui.input.newLine → "shift+enter/ctrl+j", tui.select.cancel → "escape/ctrl+c".
	hint := KeyHint("enter", "submit") + "  " +
		KeyHint("shift+enter/ctrl+j", "newline") + "  " +
		KeyHint("escape/ctrl+c", "cancel")
	lines = append(lines, NewPaddedText(hint, 1, 0, nil).Render(width)...)
	return framedDialog(width, lines)
}
