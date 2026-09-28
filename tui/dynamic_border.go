package tui

import (
	"cmp"
	"strings"
)

// dynamic_border.go: horizontal rule border component.

// DynamicBorder renders a full-width horizontal rule using "─".
type DynamicBorder struct {
	invalidatable
	color string // ANSI fg escape; empty = use theme border color
}

// NewDynamicBorder creates a border. If color is empty, the active
// theme's border color is used at render time.
func NewDynamicBorder(color string) *DynamicBorder {
	return &DynamicBorder{color: color}
}

// Render produces a single line of "─" repeated to fill width.
func (d *DynamicBorder) Render(width int) []string {
	color := cmp.Or(d.color, ActiveTheme().Border)
	w := max(1, width)
	line := color + strings.Repeat("─", w) + "\x1b[0m"
	return []string{line}
}
