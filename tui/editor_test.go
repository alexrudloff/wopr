package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/tui/widthx"
)

func TestEditorEnterSubmitsAndClears(t *testing.T) {
	e := NewEditor()
	var submitted string
	e.OnSubmit = func(text string) { submitted = text }
	e.HandleInput("hello")
	e.HandleInput("\r")
	if submitted != "hello" || e.Text() != "" {
		t.Fatalf("submitted=%q text=%q, want hello and empty", submitted, e.Text())
	}

	e.DisableSubmit = true
	submitted = ""
	e.HandleInput("x")
	e.HandleInput("\r")
	if submitted != "" || e.Text() != "x" {
		t.Fatalf("DisableSubmit: submitted=%q text=%q", submitted, e.Text())
	}
}

func TestEditorHistoryNavigation(t *testing.T) {
	e := NewEditor()
	e.AddToHistory("first")
	e.AddToHistory("second")
	e.AddToHistory("second") // consecutive duplicate skipped
	e.AddToHistory("  ")     // blank skipped
	for _, want := range []string{"second", "first", "first"} {
		e.HandleInput("\x1b[A")
		if got := e.Text(); got != want {
			t.Fatalf("up: text = %q, want %q", got, want)
		}
	}
	e.HandleInput("\x1b[B")
	e.HandleInput("\x1b[B")
	if got := e.Text(); got != "" {
		t.Fatalf("down past newest: text = %q, want empty draft", got)
	}
	e.HandleInput("\x1b[A")
	e.HandleInput("!")
	if e.inputHistIdx != -1 {
		t.Fatalf("typing did not leave history browsing (idx %d)", e.inputHistIdx)
	}
}

func TestEditorBracketedPasteSplitAcrossReads(t *testing.T) {
	e := NewEditor()
	e.HandleInput("before")
	e.HandleInput("\x1b[200~line1\nli")
	e.HandleInput("ne2\x1b[20")
	e.HandleInput("1~")
	e.HandleInput("x")
	if got, want := e.Text(), "beforeline1\nline2x"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// Large pastes collapse to a marker; the submitted (expanded) text must keep
// every pasted byte, and backspace removes the marker and its content as one.
func TestEditorLargePasteMarkerExpandsAndDeletesAtomically(t *testing.T) {
	e := NewEditor()
	var b strings.Builder
	for i := range 15 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	e.HandleInput("\x1b[200~" + b.String() + "\x1b[201~")
	if got := e.Text(); !strings.Contains(got, "[paste #1 +16 lines]") || strings.Contains(got, "line 14") {
		t.Fatalf("buffer = %q, want compact marker", got)
	}
	if got := e.GetExpandedText(); got != b.String() {
		t.Fatalf("expanded = %q, want original paste", got)
	}
	e.HandleInput("\x7f")
	if got := e.Text(); got != "" || len(e.pastes) != 0 {
		t.Fatalf("after backspace text=%q pastes=%v, want both empty", got, e.pastes)
	}
}

func TestEditorWrapsWithinWidthAndSurvivesInvalidUTF8(t *testing.T) {
	e := NewEditor()
	e.SetText(strings.Repeat("x", 250))
	for i, row := range e.Render(80) {
		if w := widthx.VisibleWidth(row); w > 80 {
			t.Fatalf("row %d width %d > 80", i, w)
		}
	}
	invalid := strings.Repeat("a", 208) + string([]byte{0xe2}) + "x"
	e.SetText(invalid)
	if rows := e.buildVisualLines(widthx.VisibleWidth(invalid)); len(rows) == 0 {
		t.Fatal("invalid UTF-8 rendered no rows")
	}
	e.HandleInput("\xff\xfe")
	_ = e.Render(20)
}
