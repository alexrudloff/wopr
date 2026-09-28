package tui

// slot_selector.go: a bordered choice list shown in the editor slot.
//
// Layout:
//
//   DynamicBorder
//   Spacer(1)
//   Text(theme.fg("accent", theme.bold(title)), 1, 0)
//   [Spacer(1) + Text(theme.fg("text", description), 1, 0)]
//   Spacer(1)
//   Container[ Text("→ option", 1, 0) | Text("  option", 1, 0) ... ]
//   Spacer(1)
//   Text("↑↓ navigate  enter select  escape/ctrl+c cancel", 1, 0)
//   Spacer(1)
//   DynamicBorder
//
// Used for /tree's "Summarize branch?", /trust, /bug, and login method
// choices.
//
// Differs from FilterableList by design: no search input box, no
// `(N/M)` scroll indicator, no per-item description column.

// SlotSelectorComponent asks the user to pick one of a list of string
// options.
type SlotSelectorComponent struct {
	invalidatable
	title                 string
	description           string
	options               []string
	cursor                int
	done                  bool
	cancel                bool
	onToggleToolsExpanded func()
}

// NewSlotSelector creates a generic selector overlay. The first
// option is pre-selected. The component does not own its lifecycle -
// the caller (runEditorSlotSelector) drives input/render
// until Done() returns true.
func NewSlotSelector(title string, options []string, onToggleToolsExpanded ...func()) *SlotSelectorComponent {
	var toggle func()
	if len(onToggleToolsExpanded) > 0 {
		toggle = onToggleToolsExpanded[0]
	}
	return &SlotSelectorComponent{
		title:                 title,
		options:               options,
		onToggleToolsExpanded: toggle,
	}
}

// SetDescription sets optional explanatory text shown between the title and
// the options.
func (e *SlotSelectorComponent) SetDescription(description string) {
	e.description = description
	e.Invalidate()
}

// Done reports whether the user picked an option (or cancelled).
func (e *SlotSelectorComponent) Done() bool { return e.done }

// Cancelled reports whether Esc was pressed.
func (e *SlotSelectorComponent) Cancelled() bool { return e.cancel }

// SelectedIndex returns the index of the selected option, or -1 if
// cancelled.
func (e *SlotSelectorComponent) SelectedIndex() int {
	if e.cancel {
		return -1
	}
	return e.cursor
}

// SelectedValue returns the selected option string, or "" if cancelled.
func (e *SlotSelectorComponent) SelectedValue() string {
	if e.cancel || e.cursor < 0 || e.cursor >= len(e.options) {
		return ""
	}
	return e.options[e.cursor]
}

// Render draws the selector as Text(…, 1, 0) rows between Spacer(1) and
// DynamicBorder rows:
//
//	DynamicBorder + Spacer + accent(bold(title)) + [Spacer + description] +
//	Spacer + one Text per option ("→ " prefix on selected) + Spacer +
//	navigate/select/cancel hint + Spacer + DynamicBorder.
//
// Every Text wraps within one cell of padding on each side and pads to width,
// so no row is wider than the render width.
func (e *SlotSelectorComponent) Render(width int) []string {
	t := ActiveTheme()
	text := func(content string) []string { return NewPaddedText(content, 1, 0, nil).Render(width) }

	var lines []string
	lines = append(lines, text(t.Accent+"\x1b[1m"+e.title+SGRBoldDimReset+t.Reset)...)
	lines = append(lines, dialogDescriptionLines(e.description, width)...)
	lines = append(lines, "")

	for i, opt := range e.options {
		if i == e.cursor {
			lines = append(lines, text(t.Accent+"→ "+t.Reset+t.Accent+opt+t.Reset)...)
		} else {
			lines = append(lines, text("  "+t.Text+opt+t.Reset)...)
		}
	}

	lines = append(lines, "")
	hint := rawArrowHint() + "  " + extKeyHint("enter", "select") + "  " + extKeyHint("escape/ctrl+c", "cancel")
	lines = append(lines, text(hint)...)
	return framedDialog(width, lines)
}

// HandleInput processes navigation, select, and cancel keys: Up/Down/k/j
// navigate, Enter selects, Esc cancels.
func (e *SlotSelectorComponent) HandleInput(data string) {
	if e.done {
		return
	}
	kb := Keybindings()
	switch {
	case kb.Matches(data, KBSelectCancel):
		e.done = true
		e.cancel = true
	case kb.Matches(data, KBSelectUp) || data == "k":
		if e.cursor > 0 {
			e.cursor--
		}
	case kb.Matches(data, KBSelectDown) || data == "j":
		if e.cursor < len(e.options)-1 {
			e.cursor++
		}
	case kb.Matches(data, KBSelectConfirm) || data == "\n":
		e.done = true
	// app.tools.expand is an app-level binding (default Ctrl+O), not part of the
	// TUI keybinding registry. Slot selectors run in modal editor-slot flows
	// outside the interactive-mode app bindings, so match the default byte
	// sequence here.
	case matchesAppToolsExpand(data):
		if e.onToggleToolsExpanded != nil {
			e.onToggleToolsExpanded()
		}
	}
	e.Invalidate()
}

func matchesAppToolsExpand(data string) bool {
	return data == "\x0f"
}

// rawArrowHint formats the "↑↓ navigate" hint like
// RawKeyHint("↑↓", "navigate") with muted-label theming.
func rawArrowHint() string {
	t := ActiveTheme()
	return t.Muted + "↑↓" + t.Reset + t.Muted + " navigate" + t.Reset
}

// extKeyHint formats "<key> <label>" with muted theming for the hint
// line in the slot selector. The keys passed in are resolved
// keybinding names ("enter", "escape/ctrl+c").
func extKeyHint(key, label string) string {
	t := ActiveTheme()
	return t.Muted + key + t.Reset + t.Muted + " " + label + t.Reset
}

// dialogDescriptionLines renders an optional dialog description as
// a blank spacer row, then the text in the theme's text color, wrapped
// with one cell of horizontal padding.
// framedDialog wraps a dialog body in the shared frame: a border and a blank
// row above and below it.
func framedDialog(width int, body []string) []string {
	border := NewDynamicBorder("").Render(width)
	lines := append(append(border, ""), body...)
	return append(append(lines, ""), border...)
}

func dialogDescriptionLines(description string, width int) []string {
	if description == "" {
		return nil
	}
	t := ActiveTheme()
	return append([]string{""}, NewPaddedText(t.Text+description+t.Reset, 1, 0, nil).Render(width)...)
}
