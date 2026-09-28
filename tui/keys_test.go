package tui

import "testing"

func TestDecodeKittyAndModifyOtherKeysText(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"\x1b[97;2u", "A", true},
		{"\x1b[49:33;2u", "!", true},
		{"\x1b[57399u", "0", true},
		{"\x1b[97;5u", "", false},
		{"\x1b[13u", "", false},
		{"\x1b[27;2;196~", "Ä", true},
		{"\x1b[27;5;97~", "", false},
	}
	for _, tc := range cases {
		if got, ok := DecodePrintableKey(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("DecodePrintableKey(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// Shift+letter arrives as CSI-u / modifyOtherKeys; the editor once dropped it.
func TestEditorInsertsShiftedLettersFromCSIu(t *testing.T) {
	e := NewEditor()
	e.HandleInput("h")
	e.HandleInput("\x1b[105;2u")
	e.HandleInput("\x1b[27;2;73~")
	if got := e.Text(); got != "hII" {
		t.Fatalf("editor text = %q, want hII", got)
	}
}

type plainComponent struct{}

func (plainComponent) Render(int) []string { return nil }
func (plainComponent) Invalidate()         {}

func TestShouldDeliverKeyDropsReleasesButNotPaste(t *testing.T) {
	if ShouldDeliverKey(plainComponent{}, "\x1b[1;1:3B") {
		t.Fatal("key release delivered to component that did not opt in")
	}
	if !ShouldDeliverKey(plainComponent{}, "\x1b[200~:3B\x1b[201~") {
		t.Fatal("paste containing release-like bytes was dropped")
	}
	if ShouldDeliverKey(nil, "\x1b[1;1:3B") {
		t.Fatal("release delivered to nil component")
	}
}
