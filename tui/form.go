package tui

import (
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// FormField is one row of a Form: a line of text, or a choice among options
// cycled with left and right.
type FormField struct {
	Label string
	// Hint is muted text under the field while it is focused.
	Hint string
	// Section, when set, starts a group: a heading drawn above the field.
	Section string
	// Options makes the field a choice; empty makes it a text entry.
	Options []string
	// Choice is the chosen option's index.
	Choice int
	// check draws a choice between off and on as a checkbox.
	check bool

	input *TextInput
}

// NewTextField returns a text entry starting at value; masked draws its
// characters as bullets.
func NewTextField(label, value, placeholder string, masked bool) *FormField {
	prompt := ""
	input := NewInput(InputOptions{Prompt: &prompt, Placeholder: placeholder})
	input.SetText(value)
	input.Mask = masked
	return &FormField{Label: label, input: input}
}

// NewChoiceField returns a choice among options, starting at choice.
func NewChoiceField(label string, options []string, choice int) *FormField {
	return &FormField{Label: label, Options: options, Choice: max(0, min(choice, len(options)-1))}
}

// NewCheckField returns a checkbox: space, left, or right toggles it.
func NewCheckField(label string, checked bool) *FormField {
	f := NewChoiceField(label, []string{"[ ]", "[x]"}, 0)
	f.check = true
	if checked {
		f.Choice = 1
	}
	return f
}

// Checked reports whether a checkbox is checked.
func (f *FormField) Checked() bool { return f.check && f.Choice == 1 }

// Value is the text entered, or the chosen option.
func (f *FormField) Value() string {
	if f.input != nil {
		return strings.TrimSpace(f.input.Text())
	}
	if f.Choice < len(f.Options) {
		return f.Options[f.Choice]
	}
	return ""
}

// Form is a dialog body of labeled fields and two buttons: a bold title,
// optional intro lines, the fields, an error line, then Save and Cancel.
// Tab, shift+tab, up, and down move through the fields and the buttons;
// enter moves on from a field and presses a button; ctrl+s presses Save
// from anywhere; esc presses Cancel. Nothing is submitted until Save is
// pressed.
type Form struct {
	invalidatable
	Title string
	// Intro lines are drawn under the title, already styled, wrapped to
	// the width; IntroFunc, when set, draws them for a width instead.
	Intro     []string
	IntroFunc func(width int) []string
	// Fields are the rows, in order.
	Fields []*FormField
	// Submit and Cancel label the buttons (default "Save" and "Cancel").
	Submit string
	Cancel string
	// Extra are more buttons after Cancel (for example "Remove this
	// model"); Pressed reports which one closed the form.
	Extra   []string
	pressed string
	// Error is drawn in the error color above the hints.
	Error string
	// MaxHeight, when set, is the rows the form may take: when it would be
	// taller, the intro goes first, then blank spacer rows, so the fields,
	// the focused hint, and the key hints stay.
	MaxHeight int

	focus           int
	done, cancelled bool
}

// NewForm returns a form over fields.
func NewForm(title string, fields ...*FormField) *Form {
	return &Form{Title: title, Fields: fields}
}

// Done reports whether the form was submitted or cancelled.
func (f *Form) Done() bool { return f.done }

// Cancelled reports whether the form was cancelled.
func (f *Form) Cancelled() bool { return f.cancelled }

// Reopen clears a submission so the form can run again, for example after
// a validation error.
func (f *Form) Reopen() { f.done, f.cancelled, f.pressed = false, false, "" }

// Pressed is the Extra button that closed the form, or "".
func (f *Form) Pressed() string { return f.pressed }

// sectioned reports whether the fields are grouped under headings.
func (f *Form) sectioned() bool {
	for _, field := range f.Fields {
		if field.Section != "" {
			return true
		}
	}
	return false
}

// Focus moves the cursor to field i.
func (f *Form) Focus(i int) { f.focus = max(0, min(i, len(f.Fields)-1)) }

// The focus runs over the fields, then the Save and Cancel buttons.
func (f *Form) saveButton() int   { return len(f.Fields) }
func (f *Form) cancelButton() int { return len(f.Fields) + 1 }

func (f *Form) move(delta int) {
	n := len(f.Fields) + 2 + len(f.Extra)
	f.focus = (f.focus + delta + n) % n
}

// HandleInput moves between fields, edits the focused one, submits, or
// cancels.
func (f *Form) HandleInput(data string) {
	defer f.Invalidate()
	switch {
	case MatchesKeyID(data, "escape"), MatchesKeyID(data, "ctrl+c"):
		f.done, f.cancelled = true, true
		return
	case MatchesKeyID(data, "ctrl+s"):
		f.done = true
		return
	case MatchesKeyID(data, "shift+tab"), MatchesKeyID(data, "up"):
		f.move(-1)
		return
	case MatchesKeyID(data, "tab"), MatchesKeyID(data, "down"):
		f.move(1)
		return
	case MatchesKeyID(data, "enter"):
		switch f.focus {
		case f.saveButton():
			f.done = true
		case f.cancelButton():
			f.done, f.cancelled = true, true
		default:
			if extra := f.focus - f.cancelButton() - 1; extra >= 0 {
				f.done, f.pressed = true, f.Extra[extra]
				return
			}
			f.move(1)
		}
		return
	}
	if f.focus >= len(f.Fields) {
		// Left and right move between the buttons.
		switch {
		case MatchesKeyID(data, "left") && f.focus > f.saveButton():
			f.focus--
		case MatchesKeyID(data, "right") && f.focus < f.cancelButton()+len(f.Extra):
			f.focus++
		}
		return
	}
	field := f.Fields[f.focus]
	if field.input != nil {
		field.input.HandleInput(data)
		return
	}
	switch {
	case MatchesKeyID(data, "left"):
		field.Choice = (field.Choice - 1 + len(field.Options)) % max(1, len(field.Options))
	case MatchesKeyID(data, "right"), MatchesKeyID(data, "space"):
		field.Choice = (field.Choice + 1) % max(1, len(field.Options))
	}
}

// Render draws the form body; the host supplies the panel background.
// Each field is one row, its label in a column; the focused field's hint
// shows under it.
func (f *Form) Render(width int) []string {
	out := f.render(width, true)
	if f.MaxHeight > 0 && len(out) > f.MaxHeight {
		out = f.render(width, false)
	}
	for i := len(out) - 2; f.MaxHeight > 0 && len(out) > f.MaxHeight && i > 1; i-- {
		if out[i] == "" {
			out = append(out[:i], out[i+1:]...)
		}
	}
	return out
}

func (f *Form) render(width int, withIntro bool) []string {
	th := ActiveTheme()
	inner := max(1, width-2*dialogPadX)
	pad := strings.Repeat(" ", dialogPadX)
	title := "\x1b[1m" + th.FgText("text", widthx.TruncateToWidth(f.Title, max(1, inner-4), "…", false)) + SGRBoldDimReset
	gap := max(1, inner-widthx.VisibleWidth(title)-3)
	out := []string{pad + title + strings.Repeat(" ", gap) + th.FgText("textMuted", "esc"), ""}
	intro := f.Intro
	if f.IntroFunc != nil {
		intro = f.IntroFunc(inner)
	}
	if !withIntro {
		intro = nil
	}
	for _, line := range intro {
		for _, wrapped := range widthx.WrapTextWithAnsi(line, inner) {
			out = append(out, pad+wrapped)
		}
	}
	if len(intro) > 0 {
		out = append(out, "")
	}
	labelWidth := 0
	for _, field := range f.Fields {
		labelWidth = max(labelWidth, widthx.VisibleWidth(field.Label)+2)
	}
	labelWidth = min(labelWidth, inner/3)
	for i, field := range f.Fields {
		if field.Section != "" {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, pad+"\x1b[1m"+th.FgText("accentAlt", field.Section)+SGRBoldDimReset)
		}
		focused := i == f.focus
		label := widthx.TruncateToWidth(field.Label, labelWidth-2, "…", false)
		label += strings.Repeat(" ", max(0, labelWidth-widthx.VisibleWidth(label)))
		if focused {
			label = "\x1b[1m" + th.FgText("primary", label) + SGRBoldDimReset
		} else {
			label = th.FgText("textMuted", label)
		}
		valueWidth := max(1, inner-labelWidth)
		var value string
		switch {
		case field.input != nil:
			field.input.Focused = focused
			field.input.placeholderStyle = func(text string) string { return th.FgText("textMuted", text) }
			// One column in, where a choice's first option starts.
			value = " " + field.input.Render(max(1, valueWidth-1))[0]
			if !focused {
				value = strings.ReplaceAll(widthx.StripAnsi(value), widthx.CursorMarker, "")
				if field.input.Text() == "" {
					value = th.FgText("textMuted", strings.TrimRight(value, " "))
				} else {
					value = th.FgText("text", value)
				}
			}
		case field.check:
			box := field.Options[field.Choice]
			if focused {
				value = " " + FillBackground(th.FgText("selectedListItemText", "\x1b[1m"+box+SGRBoldDimReset), widthx.VisibleWidth(box), th.Bg("primary"))
			} else {
				value = " " + th.FgText("text", box)
			}
		default:
			value = f.choiceRow(field, focused, valueWidth)
		}
		out = append(out, pad+label+value)
		if focused && field.Hint != "" {
			indent := labelWidth + 1
			for _, hint := range wrapText(field.Hint, max(1, inner-indent)) {
				out = append(out, pad+strings.Repeat(" ", indent)+th.FgText("textMuted", hint))
			}
		}
		// Sectioned forms pack their rows; plain ones space them.
		if !f.sectioned() || i == len(f.Fields)-1 {
			out = append(out, "")
		}
	}
	if f.Error != "" {
		for _, line := range wrapText(f.Error, inner) {
			out = append(out, pad+th.FgText("error", line))
		}
		out = append(out, "")
	}
	out = append(out, pad+f.buttons())
	return append(out, "")
}

// buttons draws Save and Cancel, the focused one highlighted, and the
// keys that reach them.
func (f *Form) buttons() string {
	th := ActiveTheme()
	button := func(label string, focused bool) string {
		if focused {
			return FillBackground(" "+"\x1b[1m"+th.FgText("selectedListItemText", label)+SGRBoldDimReset+" ", len([]rune(label))+2, th.Bg("primary"))
		}
		return FillBackground(" "+th.FgText("text", label)+" ", len([]rune(label))+2, th.Bg("backgroundElement"))
	}
	save, cancel := f.Submit, f.Cancel
	if save == "" {
		save = "Save"
	}
	if cancel == "" {
		cancel = "Cancel"
	}
	keys := "ctrl+s " + strings.ToLower(save) + " · esc " + strings.ToLower(cancel)
	if f.focus < len(f.Fields) {
		keys = "tab next · " + keys
		if field := f.Fields[f.focus]; field.input == nil && len(field.Options) > 1 {
			keys = "←→ change · " + keys
		}
	}
	var row strings.Builder
	row.WriteString(button(save, f.focus == f.saveButton()) + "  " + button(cancel, f.focus == f.cancelButton()))
	for i, extra := range f.Extra {
		row.WriteString("  " + button(extra, f.focus == f.cancelButton()+1+i))
	}
	if len(f.Extra) > 0 {
		return row.String()
	}
	return row.String() + "   " + th.FgText("textMuted", keys)
}

// choiceRow draws a choice's options on one line, the chosen one
// highlighted, scrolled so the choice stays visible.
func (f *Form) choiceRow(field *FormField, focused bool, width int) string {
	th := ActiveTheme()
	var parts []string
	for i, option := range field.Options {
		switch {
		case i == field.Choice && focused:
			parts = append(parts, FillBackground(" "+th.FgText("selectedListItemText", "\x1b[1m"+option+SGRBoldDimReset)+" ", widthx.VisibleWidth(option)+2, th.Bg("primary")))
		case i == field.Choice:
			parts = append(parts, " "+th.FgText("text", "\x1b[1m"+option+SGRBoldDimReset)+" ")
		default:
			parts = append(parts, " "+th.FgText("textMuted", option)+" ")
		}
	}
	row := strings.Join(parts, " ")
	if widthx.VisibleWidth(row) <= width {
		return row
	}
	// Too wide: show the choice with its neighbors' count.
	return th.FgText("textMuted", "‹ ") + strings.TrimSpace(parts[field.Choice]) + th.FgText("textMuted", " ›")
}
