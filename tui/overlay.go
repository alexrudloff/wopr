package tui

import (
	"slices"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// OverlayOptions places an overlay on the screen.
type OverlayOptions struct {
	// Width is the overlay width in cells; WidthPercent, when Width is 0, a
	// percentage of the terminal width. The default is 80 cells. MinWidth
	// raises either.
	Width        int
	WidthPercent float64
	MinWidth     int
	// MaxHeight caps the overlay's rows (0 means no cap).
	MaxHeight int
	// Anchor is center (the default), top-left, top-center, top-right,
	// bottom-left, bottom-center, bottom-right, left-center, or right-center.
	Anchor string
	// Margin keeps the overlay off the terminal edges.
	Margin OverlayMargin
	// NonCapturing overlays (toasts) never take keyboard focus.
	NonCapturing bool
}

// OverlayMargin is a per-edge overlay margin in cells.
type OverlayMargin struct {
	Top, Right, Bottom, Left int
}

// OverlayBounds is an overlay's last rendered terminal-relative rectangle.
type OverlayBounds struct {
	Row, Col, Width, Height int
}

type overlayID uint64

type overlayEntry struct {
	id        overlayID
	component Component
	opts      OverlayOptions
	// preFocus is the focus target when the overlay opened; closing a
	// focused overlay returns focus there.
	preFocus Component
}

// renderedOverlay is where the last frame painted an overlay.
type renderedOverlay struct {
	id        overlayID
	component Component
	bounds    OverlayBounds
}

// OverlayHandle closes and queries one overlay.
type OverlayHandle struct {
	tui *TUI
	id  overlayID
}

// OpenOverlay paints c over the screen, above earlier overlays. A capturing
// overlay takes keyboard focus. Callers Close it when done.
func (t *TUI) OpenOverlay(c Component, opts OverlayOptions) *OverlayHandle {
	t.overlayMu.Lock()
	t.nextOverlayID++
	entry := &overlayEntry{id: t.nextOverlayID, component: c, opts: opts, preFocus: t.focusTarget}
	t.overlays = append(t.overlays, entry)
	if !opts.NonCapturing {
		t.focusTarget = c
	}
	t.overlayMu.Unlock()
	t.Invalidate()
	return &OverlayHandle{tui: t, id: entry.id}
}

// Close removes the overlay. When it had focus, focus returns to the
// topmost remaining capturing overlay, or to where it was when this one
// opened.
func (h *OverlayHandle) Close() {
	t := h.tui
	t.overlayMu.Lock()
	i := slices.IndexFunc(t.overlays, func(e *overlayEntry) bool { return e.id == h.id })
	if i < 0 {
		t.overlayMu.Unlock()
		return
	}
	removed := t.overlays[i]
	t.overlays = slices.Delete(t.overlays, i, i+1)
	for _, entry := range t.overlays {
		if entry.preFocus == removed.component {
			entry.preFocus = removed.preFocus
		}
	}
	if t.focusTarget == removed.component {
		t.focusTarget = removed.preFocus
		for _, entry := range slices.Backward(t.overlays) {
			if !entry.opts.NonCapturing {
				t.focusTarget = entry.component
				break
			}
		}
	}
	t.overlayMu.Unlock()
	t.Invalidate()
}

// Bounds returns where the last frame painted the overlay.
func (h *OverlayHandle) Bounds() (OverlayBounds, bool) {
	h.tui.overlayMu.Lock()
	defer h.tui.overlayMu.Unlock()
	for _, rendered := range h.tui.renderedOverlays {
		if rendered.id == h.id {
			return rendered.bounds, true
		}
	}
	return OverlayBounds{}, false
}

// IsFocused reports whether the overlay has keyboard focus.
func (h *OverlayHandle) IsFocused() bool {
	h.tui.overlayMu.Lock()
	defer h.tui.overlayMu.Unlock()
	for _, entry := range h.tui.overlays {
		if entry.id == h.id {
			return h.tui.focusTarget == entry.component
		}
	}
	return false
}

// SetFocus gives keyboard focus to component.
func (t *TUI) SetFocus(component Component) {
	t.overlayMu.Lock()
	t.focusTarget = component
	t.overlayMu.Unlock()
}

// FocusedComponent returns the keyboard focus target.
func (t *TUI) FocusedComponent() Component {
	t.overlayMu.Lock()
	defer t.overlayMu.Unlock()
	return t.focusTarget
}

// HasOverlay reports whether any overlay is open.
func (t *TUI) HasOverlay() bool {
	t.overlayMu.Lock()
	defer t.overlayMu.Unlock()
	return len(t.overlays) > 0
}

// isOverlayFocused reports whether an overlay has keyboard focus.
func (t *TUI) isOverlayFocused() bool {
	t.overlayMu.Lock()
	defer t.overlayMu.Unlock()
	return slices.ContainsFunc(t.overlays, func(e *overlayEntry) bool { return e.component == t.focusTarget })
}

// overlayComponents returns the open overlays, bottom to top.
func (t *TUI) overlayComponents() []Component {
	t.overlayMu.Lock()
	defer t.overlayMu.Unlock()
	out := make([]Component, len(t.overlays))
	for i, entry := range t.overlays {
		out[i] = entry.component
	}
	return out
}

// composeOverlayLines paints the open overlays over background, a screen of
// width by height cells, and records where each landed.
func (t *TUI) composeOverlayLines(background []string, width, height int) []string {
	t.overlayMu.Lock()
	entries := slices.Clone(t.overlays)
	t.overlayMu.Unlock()
	if len(entries) == 0 {
		t.overlayMu.Lock()
		t.renderedOverlays = nil
		t.overlayMu.Unlock()
		return background
	}
	result := slices.Clone(background)
	type painted struct {
		lines              []string
		row, col, colWidth int
	}
	var paints []painted
	var rendered []renderedOverlay
	minimumLines := len(result)
	for _, entry := range entries {
		initial := resolveOverlayLayout(entry.opts, 0, width, height)
		lines := entry.component.Render(initial.width)
		if initial.maxHeight > 0 && len(lines) > initial.maxHeight {
			lines = lines[:initial.maxHeight]
		}
		final := resolveOverlayLayout(entry.opts, len(lines), width, height)
		paints = append(paints, painted{lines: lines, row: final.row, col: final.col, colWidth: final.width})
		rendered = append(rendered, renderedOverlay{
			id: entry.id, component: entry.component,
			bounds: OverlayBounds{Row: final.row, Col: final.col, Width: final.width, Height: len(lines)},
		})
		minimumLines = max(minimumLines, final.row+len(lines))
	}
	workingHeight := max(len(result), height, minimumLines)
	for len(result) < workingHeight {
		result = append(result, "")
	}
	viewportStart := max(0, workingHeight-height)
	for _, paint := range paints {
		for row, line := range paint.lines {
			index := viewportStart + paint.row + row
			if index < 0 || index >= len(result) || widthx.IsImageLine(result[index]) {
				continue
			}
			result[index] = compositeTuiLine(result[index], line, paint.col, paint.colWidth, width)
		}
	}
	t.overlayMu.Lock()
	t.renderedOverlays = rendered
	t.overlayMu.Unlock()
	return result
}

type overlayLayout struct {
	width, row, col, maxHeight int
}

// resolveOverlayLayout places an overlay of height rows (0 while measuring)
// on a termWidth by termHeight screen.
func resolveOverlayLayout(opts OverlayOptions, height, termWidth, termHeight int) overlayLayout {
	margin := opts.Margin
	margin.Top, margin.Right = max(0, margin.Top), max(0, margin.Right)
	margin.Bottom, margin.Left = max(0, margin.Bottom), max(0, margin.Left)
	availWidth := max(1, termWidth-margin.Left-margin.Right)
	availHeight := max(1, termHeight-margin.Top-margin.Bottom)

	width := opts.Width
	switch {
	case width > 0:
	case opts.WidthPercent > 0:
		width = int(float64(termWidth) * opts.WidthPercent / 100)
	default:
		width = min(80, availWidth)
	}
	width = max(1, min(max(width, opts.MinWidth), availWidth))

	maxHeight := 0
	if opts.MaxHeight > 0 {
		maxHeight = max(1, min(opts.MaxHeight, availHeight))
		height = min(height, maxHeight)
	}

	row := margin.Top + (availHeight-height)/2
	col := margin.Left + (availWidth-width)/2
	switch opts.Anchor {
	case "top-left", "top-center", "top-right":
		row = margin.Top
	case "bottom-left", "bottom-center", "bottom-right":
		row = margin.Top + availHeight - height
	}
	switch opts.Anchor {
	case "top-left", "left-center", "bottom-left":
		col = margin.Left
	case "top-right", "right-center", "bottom-right":
		col = margin.Left + availWidth - width
	}
	row = max(margin.Top, min(row, termHeight-margin.Bottom-height))
	col = max(margin.Left, min(col, termWidth-margin.Right-width))
	return overlayLayout{width: width, row: row, col: col, maxHeight: maxHeight}
}
