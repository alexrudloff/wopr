package tui

import (
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// DialogOption is one row of a DialogSelect.
type DialogOption struct {
	Title       string
	Description string
	// Category groups rows under a header; rows keep their given order
	// within a category, and categories appear in order of first use.
	Category string
	// Footer is right-aligned muted text, typically a key binding.
	Footer string
	Value  string
	// Details are extra muted lines under the row.
	Details []string
	// Spaced puts an empty line before the row, setting it apart (for
	// example a Back row after a list).
	Spaced bool
	// Pinned rows (actions such as Next, Cancel, Back) stay below the
	// list: they never scroll away, search never hides them, and they come
	// last in the arrow-key order. Tab jumps to them.
	Pinned bool
}

// DialogAction is a key-triggered action on the selected option, listed in
// the dialog's footer as "Title key".
type DialogAction struct {
	Title string
	// Key is the key id matched with MatchesKeyID and shown in the footer.
	Key string
	Run func(option DialogOption)
	// Silent leaves the action out of the footer (the dialog's intro
	// already says it).
	Silent bool
}

// DialogSelect is a searchable, grouped list in a dialog: a bold title with
// "esc" on the right, a search field, category headers in the accent color,
// rows with right-aligned footers, the selection on the primary color, and
// the current value marked "●".
type DialogSelect struct {
	invalidatable
	Title       string
	Placeholder string
	Current     string
	// Flat drops category headers while searching and shows each row's
	// category as its footer.
	Flat bool
	// Filter, when set, replaces the built-in fuzzy filter: it receives the
	// query and returns the options to show.
	Filter  func(query string, options []DialogOption) []DialogOption
	OnMove  func(option DialogOption)
	Actions []DialogAction
	// Hints are static footer entries, "Title label".
	Hints [][2]string
	// TermHeight reports the terminal height, which caps the list height.
	TermHeight func() int
	// Intro lines are muted text under the title, wrapped to the width.
	Intro []string
	// MaxHeight, when set, is the most rows the dialog may take: the list
	// shrinks so the title, intro, search, pinned rows, and footer always
	// fit. It takes precedence over TermHeight.
	MaxHeight int
	// appended are the rows added with AppendOption; SetOptions keeps them.
	appended []DialogOption

	options   []DialogOption
	visible   []DialogOption
	input     *TextInput
	selected  int
	scrollTop int
	// centerSelection centers the selection on the next render, after a
	// keyboard move.
	centerSelection bool
	done            bool
	cancelled       bool
	chosen          DialogOption
}

// NewDialogSelect builds a dialog over options with current preselected.
func NewDialogSelect(title string, options []DialogOption, current string) *DialogSelect {
	prompt := ""
	d := &DialogSelect{Title: title, Placeholder: "Search", Current: current, options: options}
	d.input = NewInput(InputOptions{Prompt: &prompt, Placeholder: "Search"})
	d.refilter()
	if i := slices.IndexFunc(d.visible, func(o DialogOption) bool { return o.Value == current }); i >= 0 {
		d.selected = i
		d.centerSelection = true
	}
	return d
}

// SetOptions replaces the options, keeping the selected value when it
// remains.
func (d *DialogSelect) SetOptions(options []DialogOption) {
	var keep string
	if d.selected < len(d.visible) {
		keep = d.visible[d.selected].Value
	}
	d.options = append(slices.Clone(options), d.appended...)
	d.refilter()
	if i := slices.IndexFunc(d.visible, func(o DialogOption) bool { return o.Value == keep }); i >= 0 {
		d.selected = i
	} else {
		d.selected = min(d.selected, max(0, len(d.visible)-1))
	}
	d.Invalidate()
}

// AppendOption adds a row at the end, outside any category (for example a
// Back or Cancel row). It stays through later SetOptions calls.
func (d *DialogSelect) AppendOption(option DialogOption) {
	d.appended = append(d.appended, option)
	d.options = append(d.options, option)
	d.refilter()
	d.Invalidate()
}

// Dismiss closes the dialog without a choice.
func (d *DialogSelect) Dismiss() { d.done, d.cancelled = true, true }

// Select highlights the option with value, if it is shown.
func (d *DialogSelect) Select(value string) {
	if i := slices.IndexFunc(d.visible, func(o DialogOption) bool { return o.Value == value }); i >= 0 {
		d.selected = i
		d.centerSelection = true
	}
}

// Done reports whether an option was chosen or the dialog was dismissed.
func (d *DialogSelect) Done() bool { return d.done }

// Cancelled reports whether the dialog was dismissed.
func (d *DialogSelect) Cancelled() bool { return d.cancelled }

// Chosen is the option chosen with enter.
func (d *DialogSelect) Chosen() DialogOption { return d.chosen }

// Selected is the highlighted option, if any.
func (d *DialogSelect) Selected() (DialogOption, bool) {
	if d.selected < 0 || d.selected >= len(d.visible) {
		return DialogOption{}, false
	}
	return d.visible[d.selected], true
}

func (d *DialogSelect) query() string { return strings.TrimSpace(d.input.Text()) }

// searchMinOptions is the list length from which the dialog offers search;
// shorter lists are read at a glance.
const searchMinOptions = 9

// searchable reports whether the dialog shows its search field.
func (d *DialogSelect) searchable() bool {
	return len(d.options) >= searchMinOptions || d.Filter != nil
}

func (d *DialogSelect) refilter() {
	query := d.query()
	var listed, pinned []DialogOption
	for _, option := range d.options {
		if option.Pinned {
			pinned = append(pinned, option)
		} else {
			listed = append(listed, option)
		}
	}
	defer func() { d.visible = append(d.visible, pinned...) }()
	switch {
	case d.Filter != nil:
		d.visible = d.Filter(query, listed)
	case query == "":
		d.visible = slices.Clone(listed)
	default:
		type scored struct {
			option DialogOption
			score  float64
		}
		var matches []scored
		for _, option := range listed {
			title := FuzzyMatchScore(query, option.Title)
			category := FuzzyMatchScore(query, option.Category)
			if !title.Matches && !category.Matches {
				continue
			}
			// Lower is better; a title match counts double a category match.
			score := 0.0
			if title.Matches {
				score += title.Score - 1000
			}
			if category.Matches {
				score += (category.Score - 1000) / 2
			}
			matches = append(matches, scored{option, score})
		}
		slices.SortStableFunc(matches, func(a, b scored) int {
			switch {
			case a.score < b.score:
				return -1
			case a.score > b.score:
				return 1
			}
			return 0
		})
		d.visible = make([]DialogOption, len(matches))
		for i, match := range matches {
			d.visible[i] = match.option
		}
	}
	if !d.flatNow() {
		d.visible = groupByCategory(d.visible)
	}
}

func (d *DialogSelect) flatNow() bool { return d.Flat && d.query() != "" }

// groupByCategory orders options by category of first appearance, keeping
// their order within each category.
func groupByCategory(options []DialogOption) []DialogOption {
	var order []string
	groups := map[string][]DialogOption{}
	for _, option := range options {
		if _, ok := groups[option.Category]; !ok {
			order = append(order, option.Category)
		}
		groups[option.Category] = append(groups[option.Category], option)
	}
	out := make([]DialogOption, 0, len(options))
	for _, category := range order {
		out = append(out, groups[category]...)
	}
	return out
}

func (d *DialogSelect) move(delta int) {
	n := len(d.visible)
	if n == 0 {
		return
	}
	next := d.selected + delta
	switch {
	case next < 0:
		next = n - 1
	case next >= n:
		next = 0
	}
	d.selected = next
	d.centerSelection = true
	if d.OnMove != nil {
		d.OnMove(d.visible[next])
	}
}

// HandleInput navigates, filters, chooses, or dismisses.
func (d *DialogSelect) HandleInput(data string) {
	switch {
	case MatchesKeyID(data, "escape"), MatchesKeyID(data, "ctrl+c"):
		d.done, d.cancelled = true, true
	case MatchesKeyID(data, "up"), MatchesKeyID(data, "ctrl+p"):
		d.move(-1)
	case MatchesKeyID(data, "down"), MatchesKeyID(data, "ctrl+n"):
		d.move(1)
	case MatchesKeyID(data, "pageUp"):
		d.move(-10)
	case MatchesKeyID(data, "pageDown"):
		d.move(10)
	case MatchesKeyID(data, "tab") && d.pinnedCount() > 0:
		// To the next pinned row, from the list to the first one.
		first := len(d.visible) - d.pinnedCount()
		if d.selected < first || d.selected >= len(d.visible)-1 {
			d.selected = first
		} else {
			d.selected++
		}
	case MatchesKeyID(data, "home"):
		d.selected = 0
	case MatchesKeyID(data, "end"):
		d.selected = max(0, len(d.visible)-1)
	case MatchesKeyID(data, "enter"):
		if option, ok := d.Selected(); ok {
			d.chosen, d.done = option, true
		}
	default:
		for _, action := range d.Actions {
			if action.Key != "" && MatchesKeyID(data, action.Key) {
				if option, ok := d.Selected(); ok && action.Run != nil {
					action.Run(option)
				}
				d.Invalidate()
				return
			}
		}
		if !d.searchable() {
			d.Invalidate()
			return
		}
		before := d.input.Text()
		d.input.HandleInput(data)
		if d.input.Text() != before {
			d.refilter()
			d.selected, d.scrollTop = 0, 0
			if d.query() == "" {
				if i := slices.IndexFunc(d.visible, func(o DialogOption) bool { return o.Value == d.Current }); i >= 0 {
					d.selected = i
					d.centerSelection = true
				}
			}
		}
	}
	d.Invalidate()
}

// dialogPadX is the inset of the title, search field, headers, and footer.
const dialogPadX = 4

// Render draws the dialog body; the host supplies the panel background.
func (d *DialogSelect) Render(width int) []string {
	th := ActiveTheme()
	inner := max(1, width-2*dialogPadX)
	pad := strings.Repeat(" ", dialogPadX)
	title := "\x1b[1m" + th.FgText("text", widthx.TruncateToWidth(d.Title, max(1, inner-4), "…", false)) + SGRBoldDimReset
	gap := max(1, inner-widthx.VisibleWidth(title)-3)
	out := []string{pad + title + strings.Repeat(" ", gap) + th.FgText("textMuted", "esc"), ""}
	for _, line := range d.Intro {
		for _, wrapped := range widthx.WrapTextWithAnsi(line, inner) {
			out = append(out, pad+th.FgText("textMuted", wrapped))
		}
	}
	if len(d.Intro) > 0 {
		out = append(out, "")
	}

	if d.searchable() {
		d.input.Focused = true
		d.input.placeholderStyle = func(text string) string { return th.FgText("textMuted", text) }
		if d.Placeholder != "" {
			d.input.placeholder = d.Placeholder
		}
		search := d.input.Render(inner)
		if len(search) > 0 {
			out = append(out, pad+th.FgText("textMuted", search[0]))
		}
		out = append(out, "")
	}

	rows, selectedRow := d.listRows(width)
	pinned := d.pinnedRows(width)
	footer := d.footer()
	height := len(rows)
	switch {
	case d.MaxHeight > 0:
		// Everything but the list: what is drawn so far, the pinned rows,
		// the footer and its spacing, and the closing blank row.
		fixed := len(out) + len(pinned) + 1
		if footer != "" {
			fixed += 2
		}
		height = min(height, max(1, d.MaxHeight-fixed))
	case d.TermHeight != nil:
		height = min(height, max(3, d.TermHeight()/2-6-len(pinned)))
	}
	if len(rows) == 0 {
		out = append(out, pad+th.FgText("textMuted", "No results found"))
	} else {
		d.scrollTo(selectedRow, height, len(rows))
		out = append(out, rows[d.scrollTop:min(len(rows), d.scrollTop+height)]...)
	}
	out = append(out, pinned...)

	if footer != "" {
		out = append(out, "", pad+footer)
	}
	return append(out, "")
}

func (d *DialogSelect) scrollTo(row, height, total int) {
	if row < 0 {
		return
	}
	switch {
	case d.centerSelection:
		d.scrollTop = row - height/2
		d.centerSelection = false
	case row < d.scrollTop:
		d.scrollTop = row
	case row >= d.scrollTop+height:
		d.scrollTop = row - height + 1
	}
	d.scrollTop = max(0, min(d.scrollTop, total-height))
}

// listRows renders the category headers and rows, returning the row index of
// the selection.
func (d *DialogSelect) listRows(width int) ([]string, int) {
	th := ActiveTheme()
	flat := d.flatNow()
	selectedRow := -1
	var rows []string
	category := "\x00"
	for i, option := range d.visible[:len(d.visible)-d.pinnedCount()] {
		if !flat && option.Category != category {
			category = option.Category
			if category != "" {
				if len(rows) > 0 {
					rows = append(rows, "")
				}
				rows = append(rows, strings.Repeat(" ", dialogPadX)+"\x1b[1m"+th.FgText("accentAlt", category)+SGRBoldDimReset)
			}
		}
		if option.Spaced && len(rows) > 0 {
			rows = append(rows, "")
		}
		if i == d.selected {
			selectedRow = len(rows)
		}
		rows = append(rows, d.optionRow(option, i == d.selected, flat, width))
		for _, detail := range option.Details {
			rows = append(rows, strings.Repeat(" ", dialogPadX)+th.FgText("textMuted", widthx.TruncateToWidth(detail, max(1, width-2*dialogPadX), "…", false)))
		}
	}
	return rows, selectedRow
}

// pinnedCount is how many of the visible options are pinned (they are last).
func (d *DialogSelect) pinnedCount() int {
	n := 0
	for _, option := range d.visible {
		if option.Pinned {
			n++
		}
	}
	return n
}

// pinnedRows renders the pinned options below the list, set apart by an
// empty line.
func (d *DialogSelect) pinnedRows(width int) []string {
	first := len(d.visible) - d.pinnedCount()
	if first == len(d.visible) {
		return nil
	}
	rows := []string{""}
	for i := first; i < len(d.visible); i++ {
		if d.visible[i].Spaced && i > first {
			rows = append(rows, "")
		}
		rows = append(rows, d.optionRow(d.visible[i], i == d.selected, false, width))
	}
	return rows
}

func (d *DialogSelect) optionRow(option DialogOption, active, flat bool, width int) string {
	th := ActiveTheme()
	current := option.Value == d.Current && d.Current != ""
	footer := option.Footer
	if flat && option.Category != "" {
		footer = option.Category
	}
	rowWidth := max(1, width-2) // the row spans columns 1..width-2
	textToken, mutedToken := "text", "textMuted"
	switch {
	case active:
		textToken, mutedToken = "selectedListItemText", "selectedListItemText"
	case current:
		textToken = "primary"
	}
	marker := "   "
	if current {
		marker = " " + th.FgText(textToken, "●") + " "
	}
	footerText := ""
	if footer != "" {
		footerText = th.FgText(mutedToken, footer)
	}
	footerWidth := widthx.VisibleWidth(footer)
	titleRoom := max(1, rowWidth-3-footerWidth-3-1)
	titleText := option.Title
	body := th.FgText(textToken, titleText)
	if active {
		body = "\x1b[1m" + body + SGRBoldDimReset
	}
	if option.Description != "" && option.Description != option.Category {
		body += th.FgText(mutedToken, " "+option.Description)
	}
	body = widthx.TruncateToWidth(body, titleRoom, "…", false)
	left := marker + body
	gap := max(1, rowWidth-widthx.VisibleWidth(left)-footerWidth-3)
	row := left + strings.Repeat(" ", gap) + footerText + "   "
	if active {
		return " " + FillBackground(row, rowWidth, th.Bg("primary"))
	}
	return " " + row
}

func (d *DialogSelect) footer() string {
	th := ActiveTheme()
	var parts []string
	for _, action := range d.Actions {
		if action.Key == "" || action.Silent {
			continue
		}
		parts = append(parts, th.FgText("text", action.Title)+" "+th.FgText("textMuted", FormatKeyText(action.Key, false)))
	}
	for _, hint := range d.Hints {
		parts = append(parts, "\x1b[1m"+th.FgText("text", hint[0])+SGRBoldDimReset+" "+th.FgText("textMuted", hint[1]))
	}
	return strings.Join(parts, "  ")
}
