package tui

// BranchSummaryComponent renders a collapsible branch-summary marker.
// It keeps one column of horizontal and one row of vertical padding.

// customMsgLabelBranchFg is the fg color for the [branch] label, the same as
// [compaction]. Value reuses the already-computed constant from
// compaction_summary.go.
func customMsgLabelBranchFg() string { return customMsgLabelFg() } // #9575cd

// BranchSummaryComponent renders a branch-summary boundary marker in the chat
// transcript. Replaces the single-row BranchSummaryChip.
type BranchSummaryComponent struct {
	invalidatable
	summary  string
	expanded bool
}

// NewBranchSummaryComponent creates a branch summary component.
// summary is the LLM-generated markdown text summarising the abandoned branch.
func NewBranchSummaryComponent(summary string) *BranchSummaryComponent {
	return &BranchSummaryComponent{summary: summary}
}

// SetExpanded opens or collapses the component body. InteractiveMode calls it
// from the global Ctrl+O toggle.
func (c *BranchSummaryComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.Invalidate()
}

// Render returns the lines for this component at the given terminal width.
// All column measurements are delegated to paintBgWith (which uses
// widthx.VisibleWidth internally).
func (c *BranchSummaryComponent) Render(width int) []string {
	width = max(width, 4)

	const (
		bold    = "\x1b[1m"
		boldEnd = "\x1b[22m"
		dim     = "\x1b[2m"
		reset   = "\x1b[0m"
	)

	customMsgBgOpen := ActiveTheme().CustomMessageBg
	var out []string

	// Top padding row.
	out = append(out, paintBgWith(customMsgBgOpen, "", width))

	// Label row: " [branch]" with bold + label fg color.
	labelStyled := customMsgLabelBranchFg() + bold + "[branch]" + boldEnd + reset
	out = append(out, paintBgWith(customMsgBgOpen, " "+labelStyled, width))

	// Structural blank row between label and body.
	out = append(out, paintBgWith(customMsgBgOpen, "", width))

	if c.expanded {
		// Expanded: render "**Branch Summary**\n\n<summary>" as markdown.
		header := "**Branch Summary**\n\n"
		md := NewMarkdown(header + c.summary)
		contentWidth := max(width-2, 1)
		for _, line := range md.Render(contentWidth) {
			out = append(out, paintBgWith(customMsgBgOpen, " "+line, width))
		}
	} else {
		// Collapsed: single summary line with a dim, lowercase "ctrl+o" hint.
		// customMessageText is "" in dark.json: no extra fg color.
		body := "Branch summary (" + dim + "ctrl+o" + reset + " to expand)"
		out = append(out, paintBgWith(customMsgBgOpen, " "+body, width))
	}

	// Bottom padding row.
	out = append(out, paintBgWith(customMsgBgOpen, "", width))

	return out
}
