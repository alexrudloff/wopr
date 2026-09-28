package tui

import (
	"bytes"
	"testing"
)

type renderFuncComponent func(width int) []string

func (f renderFuncComponent) Render(width int) []string { return f(width) }
func (f renderFuncComponent) Invalidate()               {}

const overflowRow = "a b c d e f g h i j k l m n o p q r s t u v" // 43 cells

func renderRecover(r interface{ Render() }) (value any) {
	defer func() { value = recover() }()
	r.Render()
	return nil
}

// The alt screen clips over-wide rows instead of terminating.
func TestOverflowAltScreenDoesNotTerminate(t *testing.T) {
	lines := []string{"fits", overflowRow}
	ui := newAltScreenForTest(&bytes.Buffer{}, 20, 10, Options{})
	ui.Add(renderFuncComponent(func(int) []string { return lines }))
	ui.Start()
	if value := renderRecover(ui); value != nil {
		t.Fatalf("alt screen terminated: %v", value)
	}
}
