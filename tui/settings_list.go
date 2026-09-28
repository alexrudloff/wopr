package tui

// SettingsList: a two-column settings selector with value cycling.
//
// Layout:
//
//     filter: █
//
//   › Auto-compact            true
//     Steering mode           one-at-a-time
//     Follow-up mode          one-at-a-time
//     (1/13)
//
//     Automatically compact context when it gets too large
//
//     Type to search · Enter/Space to change · Esc to cancel
//
// The selected row is highlighted, values are right-aligned in a
// second column, the description of the selected item appears below
// the list, and a hint line is always shown at the bottom.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// SettingItem describes one toggle-able setting.
type SettingItem struct {
	ID           string   // unique identifier
	Label        string   // display label (left column)
	Description  string   // shown below list when selected
	CurrentValue string   // right column
	Values       []string // Enter/Space cycles through these
	Submenu      func(currentValue string, done func(*string)) Component
}

// SettingsList is a two-column selector that cycles setting values.
// After Done()==true, callers read ChangedID/ChangedValue for the last
// change, or Cancelled() if the user pressed Esc.
type SettingsList struct {
	invalidatable
	items    []SettingItem
	filtered []int // indices into items
	cursor   int
	filter   string

	maxVisible        int
	searchEnabled     bool
	mousePressedIndex int

	done      bool
	cancelled bool
	// Last change made (for the caller loop pattern).
	ChangedID    string
	ChangedValue string
	submenu      Component
}

// NewSettingsList creates the searchable settings list the /settings
// selector uses (maxVisible 10, enableSearch true).
func NewSettingsList(items []SettingItem) *SettingsList {
	return NewSettingsListWithOptions(items, 10, true)
}

// NewSettingsListWithOptions creates a settings list with maxVisible rows
// and optional search. Nested
// settings menus pass Math.min(items.length, 10) and no search.
func NewSettingsListWithOptions(items []SettingItem, maxVisible int, enableSearch bool) *SettingsList {
	s := &SettingsList{items: items, maxVisible: max(1, maxVisible), searchEnabled: enableSearch, mousePressedIndex: -1}
	s.applyFilter()
	return s
}

// Done reports whether the user confirmed a change or cancelled.
func (s *SettingsList) Done() bool { return s.done }

// Cancelled reports whether the user pressed Esc.
func (s *SettingsList) Cancelled() bool { return s.cancelled }

// Reset clears done/cancelled so the list can be reused in a loop.
func (s *SettingsList) Reset() {
	s.done = false
	s.cancelled = false
	s.ChangedID = ""
	s.ChangedValue = ""
}

// UpdateValue sets a new current value for the item with the given ID.
func (s *SettingsList) UpdateValue(id, newValue string) {
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].CurrentValue = newValue
			break
		}
	}
}

// Render draws the settings list.
func (s *SettingsList) Render(width int) []string {
	if s.submenu != nil {
		return s.submenu.Render(width)
	}
	var lines []string

	// Search input row followed by one blank line.
	if s.searchEnabled {
		lines = append(lines, renderSettingsSearchInput(s.filter, width))
		lines = append(lines, "")
	}

	display := s.displayItems()
	if len(s.items) == 0 {
		lines = append(lines, padOrTrunc(dim("  No settings available"), width))
		if s.searchEnabled {
			lines = append(lines, s.hintLines(width)...)
		}
		return lines
	}
	if len(display) == 0 {
		lines = append(lines, padOrTrunc(dim("  No matching settings"), width))
		return append(lines, s.hintLines(width)...)
	}

	// Calculate max label width for alignment.
	// Widest label, capped at 36 columns.
	maxLabelW := 0
	for _, item := range s.items {
		maxLabelW = max(maxLabelW, widthx.VisibleWidth(item.Label))
	}
	maxLabelW = min(maxLabelW, 36)

	// Visible window.
	start, end := s.visibleRange(display)

	// Accent color cursor "→ ".
	accentCursor := ThemeHexFg("#8abeb7") + "→ \x1b[0m"
	const noCursor = "  "
	th := ActiveTheme()
	muted := cmp.Or(th.Dim, "\x1b[38;2;102;102;102m")
	valueMuted := cmp.Or(th.Muted, "\x1b[38;2;128;128;128m")
	accent := cmp.Or(th.Accent, "\x1b[38;2;138;190;183m")

	for i := start; i < end; i++ {
		item := s.items[display[i]]
		selected := i == s.cursor

		var prefix string
		if selected {
			prefix = accentCursor
		} else {
			prefix = noCursor
		}

		// Pad label to maxLabelW for column alignment.
		labelPad := max(maxLabelW-widthx.VisibleWidth(item.Label), 0)
		labelCol := item.Label + strings.Repeat(" ", labelPad)

		sep := "  "
		usedWidth := widthx.VisibleWidth(prefix) + maxLabelW + widthx.VisibleWidth(sep)
		valueMaxWidth := max(1, width-usedWidth-2)
		valueStr := widthx.TruncateToWidth(item.CurrentValue, valueMaxWidth, "", false)
		labelText := labelCol
		valueText := valueMuted + valueStr + th.Reset
		if selected {
			labelText = accent + labelCol + "\x1b[39m"
			valueText = accent + valueStr + "\x1b[39m"
		}
		row := prefix + labelText + sep + valueText
		lines = append(lines, widthx.TruncateToWidth(row, width, "", false))
	}

	// Scroll indicator, e.g. "(1/13)".
	if start > 0 || end < len(display) {
		scrollText := fmt.Sprintf("  (%d/%d)", s.cursor+1, len(display))
		lines = append(lines, padOrTrunc(muted+scrollText+th.Reset, width))
	}

	// Description of selected item.
	if s.cursor >= 0 && s.cursor < len(display) {
		sel := s.items[display[s.cursor]]
		if sel.Description != "" {
			lines = append(lines, "")
			wrapped := widthx.WrapTextWithAnsi(sel.Description, width-4)
			for _, wl := range wrapped {
				lines = append(lines, padOrTrunc(muted+"  "+wl+th.Reset, width))
			}
		}
	}

	return append(lines, s.hintLines(width)...)
}

// hintLines returns the key hint footer.
func (s *SettingsList) hintLines(width int) []string {
	hint := "  Enter/Space to change · Esc to cancel"
	if s.searchEnabled {
		hint = "  Type to search · Enter/Space to change · Esc to cancel"
	}
	th := ActiveTheme()
	muted := cmp.Or(th.Dim, "\x1b[38;2;102;102;102m")
	return []string{"", padOrTrunc(muted+hint+th.Reset, width)}
}

// HandleMouse moves selection by wheel, selects a visible row on press, and
// activates the pressed row on click. Pointer motion does not change selection.
func (s *SettingsList) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if s.searchEnabled {
		if event.Y == 0 {
			if event.Type == MousePress && event.Button == MouseButtonLeft {
				return &TuiMouseDispatchResult{Handled: true, Focus: true}
			}
			return nil
		}
		if event.Y == 1 {
			return nil
		}
	}
	display := s.displayItems()
	if len(display) == 0 {
		return nil
	}
	if event.Type == MouseWheel && event.WheelDelta != 0 {
		previous := s.cursor
		if event.WheelDelta < 0 {
			s.cursor--
		} else {
			s.cursor++
		}
		s.cursor = max(0, min(s.cursor, len(display)-1))
		changed := s.cursor != previous
		if changed {
			s.Invalidate()
		}
		return &TuiMouseDispatchResult{Handled: true, Render: new(changed)}
	}
	if event.Button != MouseButtonLeft || (event.Type != MousePress && event.Type != MouseClick) {
		return nil
	}
	rowOffset := 0
	if s.searchEnabled {
		rowOffset = 2
	}
	start, end := s.visibleRange(display)
	itemIndex := start + event.Y - rowOffset
	if itemIndex < start || itemIndex >= end {
		return nil
	}
	if event.Type == MousePress {
		s.mousePressedIndex = itemIndex
		s.cursor = itemIndex
		s.Invalidate()
		return &TuiMouseDispatchResult{Handled: true, Focus: true}
	}
	if s.mousePressedIndex >= 0 {
		s.cursor = s.mousePressedIndex
	} else {
		s.cursor = itemIndex
	}
	s.mousePressedIndex = -1
	s.activateItem()
	s.Invalidate()
	return &TuiMouseDispatchResult{Handled: true}
}

func (s *SettingsList) visibleRange(display []int) (start, end int) {
	start = max(0, min(s.cursor-s.maxVisible/2, len(display)-s.maxVisible))
	end = min(start+s.maxVisible, len(display))
	return start, end
}

// HandleInput processes keystrokes.
func (s *SettingsList) HandleInput(data string) {
	if s.submenu != nil {
		if handler, ok := s.submenu.(InputHandler); ok {
			handler.HandleInput(data)
		}
		s.Invalidate()
		return
	}
	kb := Keybindings()
	display := s.displayItems()
	switch {
	case kb.Matches(data, KBSelectCancel):
		s.cancelled = true
		s.done = true
	case kb.Matches(data, KBSelectConfirm) || (data == " " && (!s.searchEnabled || s.filter == "")):
		// Space changes the value unless it types into a
		// non-empty search.
		s.activateItem()
	case kb.Matches(data, KBSelectUp):
		if len(display) > 0 {
			s.cursor = (s.cursor - 1 + len(display)) % len(display)
		}
	case kb.Matches(data, KBSelectDown):
		if len(display) > 0 {
			s.cursor = (s.cursor + 1) % len(display)
		}
	case kb.Matches(data, KBSelectPageUp):
		s.moveCursor(-10)
	case kb.Matches(data, KBSelectPageDown):
		s.moveCursor(10)
	case data == "\x1b[H":
		s.cursor = 0
	case data == "\x1b[F":
		s.cursor = max(len(display)-1, 0)
	case !s.searchEnabled:
		// Without search, every other key is ignored.
	case data == "\x7f" || data == "\b":
		if len(s.filter) > 0 {
			_, size := utf8.DecodeLastRuneInString(s.filter)
			s.filter = s.filter[:len(s.filter)-size]
			s.applyFilter()
		}
	case data == "\x15":
		s.filter = ""
		s.applyFilter()
	default:
		if len(data) >= 1 && data[0] >= 0x20 && data[0] != 0x7f {
			for _, r := range data {
				if r >= 0x20 && r != 0x7f {
					s.filter += string(r)
				}
			}
			s.applyFilter()
		}
	}
	s.Invalidate()
}

func (s *SettingsList) activateItem() {
	display := s.displayItems()
	if s.cursor < 0 || s.cursor >= len(display) {
		return
	}
	item := &s.items[display[s.cursor]]
	if item.Submenu != nil {
		s.submenu = item.Submenu(item.CurrentValue, func(value *string) {
			s.submenu = nil
			if value != nil {
				item.CurrentValue = *value
			}
			s.Invalidate()
		})
		return
	}
	if len(item.Values) == 0 {
		return
	}
	// Cycle to the next value (the first when the current one is unknown).
	next := (slices.Index(item.Values, item.CurrentValue) + 1) % len(item.Values)
	item.CurrentValue = item.Values[next]
	s.ChangedID = item.ID
	s.ChangedValue = item.CurrentValue
	s.done = true
}

func (s *SettingsList) moveCursor(delta int) {
	display := s.displayItems()
	s.cursor = clampIndex(s.cursor+delta, len(display))
}

func (s *SettingsList) displayItems() []int {
	return s.filtered
}

func (s *SettingsList) applyFilter() {
	s.filtered = s.filtered[:0]
	filtered := FuzzyFilter(s.items, s.filter, func(item SettingItem) string { return item.Label })
	for _, item := range filtered {
		for i := range s.items {
			if s.items[i].ID == item.ID {
				s.filtered = append(s.filtered, i)
				break
			}
		}
	}
	// Filtering moves the selection to the first match.
	s.cursor = 0
}

func renderSettingsSearchInput(value string, width int) string {
	prompt := "> "
	availableWidth := max(1, width-widthx.VisibleWidth(prompt))
	visibleText := widthx.TruncateToWidth(value, max(0, availableWidth-1), "", false)
	line := "\x1b[39m" + prompt + visibleText + "\x1b[7m \x1b[0m"
	return padOrTrunc(line, width)
}
