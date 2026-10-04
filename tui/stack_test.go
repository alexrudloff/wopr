package tui

import "testing"

type countingComponent struct {
	invalidatable
	widths []int
}

func (c *countingComponent) Render(width int) []string {
	c.widths = append(c.widths, width)
	return []string{"x"}
}

// A child with a basis renders once per frame, at its share. Measuring it at
// the full width first made long transcripts re-render all history twice per
// keystroke.
func TestHStackRendersBasisChildOnce(t *testing.T) {
	child := &countingComponent{}
	h := NewHStack([]StackChild{
		{Component: NewSpacer(0), Basis: new(2), Grow: new(0), Shrink: new(0)},
		{Component: child, Basis: new(0), Grow: new(1), Shrink: new(1)},
	}, StackOptions{})
	h.Render(100)
	if len(child.widths) != 1 || child.widths[0] != 98 {
		t.Fatalf("child rendered at widths %v, want [98]", child.widths)
	}
}
