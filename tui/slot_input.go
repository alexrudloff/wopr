package tui

import "cmp"

// slot_input.go: a bordered text input shown in the editor slot.
//
// Wraps the bare TextInput surface with title, key hints, and horizontal
// borders for editor-slot prompts; input semantics live in
// text_input.go.

// SlotInputComponent wraps a bare TextInput in borders, a title, and hints.
type SlotInputComponent struct {
	invalidatable
	input *TextInput
	title string
}

// NewSlotInputComponent creates the editor-slot input.
// Placeholder is accepted but currently ignored.
func NewSlotInputComponent(title, placeholder string) *SlotInputComponent {
	_ = placeholder
	return &SlotInputComponent{
		input: NewTextInput(),
		title: title,
	}
}

// Done reports whether the user submitted or cancelled the input.
func (e *SlotInputComponent) Done() bool { return e.input.Done() }

// Cancelled reports whether the user cancelled the input.
func (e *SlotInputComponent) Cancelled() bool { return e.input.Cancelled() }

// Text returns the current value.
func (e *SlotInputComponent) Text() string { return e.input.Text() }

// SetText pre-fills the input.
func (e *SlotInputComponent) SetText(s string) { e.input.SetText(s) }

// HandleInput delegates to the wrapped input.
func (e *SlotInputComponent) HandleInput(data string) {
	e.input.HandleInput(data)
	e.Invalidate()
}

// Render lays out the input: the title and the
// key hints are Text(..., 1, 0) children, so they wrap within the width.
func (e *SlotInputComponent) Render(width int) []string {
	th := ActiveTheme()
	accent := cmp.Or(th.Accent, "\x1b[38;2;138;190;183m")
	// tui.select.confirm → "enter", tui.select.cancel → "escape/ctrl+c".
	hint := RawKeyHint("enter", "submit") + "  " + RawKeyHint("escape/ctrl+c", "cancel")

	var lines []string
	lines = append(lines, NewPaddedText(accent+e.title+th.Reset, 1, 0, nil).Render(width)...)
	lines = append(lines, "")
	lines = append(lines, e.input.Render(width)...)
	lines = append(lines, "")
	lines = append(lines, NewPaddedText(hint, 1, 0, nil).Render(width)...)
	return framedDialog(width, lines)
}
