package tui

import "testing"

func TestFilterableListFilterNavigateEnter(t *testing.T) {
	f := NewFilterableList("", []string{"alpha foo", "alpha bar", "beta foo"})
	f.HandleInput("\x1b[A")
	if f.cursor != 2 {
		t.Fatalf("up from top: cursor=%d, want wrap to 2", f.cursor)
	}
	for _, r := range "al fo" {
		f.HandleInput(string(r))
	}
	if len(f.filtered) != 1 || f.cursor != 0 {
		t.Fatalf("filter tokens: filtered=%d cursor=%d, want 1 match at 0", len(f.filtered), f.cursor)
	}
	f.HandleInput("\r")
	if !f.Done() || f.Cancelled() || f.SelectedIndex() != 0 {
		t.Fatalf("enter: done=%v cancelled=%v selected=%d", f.Done(), f.Cancelled(), f.SelectedIndex())
	}
}

func TestFilterableListEscCancels(t *testing.T) {
	f := NewFilterableList("", []string{"a", "b"})
	f.HandleInput("\x1b")
	if !f.Done() || !f.Cancelled() || f.SelectedIndex() != -1 {
		t.Fatalf("done=%v cancelled=%v selected=%d", f.Done(), f.Cancelled(), f.SelectedIndex())
	}
}
