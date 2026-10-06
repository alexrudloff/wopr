package tui

// TextEditDialog edits a long text in a dialog: a multi-line editor above
// Save and Cancel. Enter is a newline; ctrl+s saves, esc cancels, and tab
// moves between the editor and the buttons.
type TextEditDialog struct {
	invalidatable
	form       *Form
	editor     *Editor
	onButtons  bool
	done, save bool
}

// NewTextEditDialog returns a dialog editing text, showing lines rows of it.
func NewTextEditDialog(title, text string, lines int) *TextEditDialog {
	d := &TextEditDialog{editor: NewEditor()}
	d.editor.DisableSubmit = true
	d.editor.SetMaxVisibleLines(lines)
	d.editor.SetText(text)
	d.editor.Focused = true
	d.form = NewForm(title)
	d.form.IntroFunc = func(width int) []string { return d.editor.Render(width) }
	return d
}

// Done reports whether the dialog closed.
func (d *TextEditDialog) Done() bool { return d.done }

// Cancelled reports whether it closed without saving.
func (d *TextEditDialog) Cancelled() bool { return d.done && !d.save }

// Text is the edited text.
func (d *TextEditDialog) Text() string { return d.editor.Text() }

// SetError shows err above the buttons and reopens the dialog.
func (d *TextEditDialog) SetError(err string) {
	d.form.Error = err
	d.form.Reopen()
	d.done, d.save = false, false
	d.Invalidate()
}

func (d *TextEditDialog) HandleInput(data string) {
	defer d.Invalidate()
	switch {
	case MatchesKeyID(data, "escape"), MatchesKeyID(data, "ctrl+c"):
		d.done = true
		return
	case MatchesKeyID(data, "ctrl+s"):
		d.done, d.save = true, true
		return
	case MatchesKeyID(data, "tab"), MatchesKeyID(data, "shift+tab"):
		d.onButtons = !d.onButtons
		d.editor.Focused = !d.onButtons
		d.form.focus = d.form.saveButton()
		return
	}
	if !d.onButtons {
		if MatchesKeyID(data, "enter") {
			data = "\n" // ctrl+j: Enter is a newline here
		}
		d.editor.HandleInput(data)
		return
	}
	d.form.HandleInput(data)
	if d.form.Done() {
		d.done, d.save = true, !d.form.Cancelled()
	}
}

func (d *TextEditDialog) Render(width int) []string {
	if !d.onButtons {
		// Focus past the buttons so neither is drawn as chosen.
		d.form.focus = d.form.cancelButton() + 1 + len(d.form.Extra)
	}
	return d.form.Render(width)
}
