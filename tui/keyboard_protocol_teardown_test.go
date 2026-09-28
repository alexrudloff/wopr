package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func resetKeyboardProtocolState(pushed, modify, kitty bool) {
	keyboardProtocolPushed.Store(pushed)
	modifyOtherKeysActive.Store(modify)
	kittyProtocolActive.Store(kitty)
}

// The Kitty pop must happen exactly once (a second pop disturbs the terminal's
// own stack) and in one write so a concurrent render cannot split it.
func TestDisableKeyboardProtocolIsIdempotentAndSingleWrite(t *testing.T) {
	var out bytes.Buffer
	term := newProcessTerminal(nil, nil, &out)
	resetKeyboardProtocolState(true, true, true)
	t.Cleanup(func() { resetKeyboardProtocolState(false, false, false) })
	term.disableKeyboardProtocol()
	if want := keyboardProtocolPop + modifyOtherKeysDisable; out.String() != want {
		t.Fatalf("teardown = %q, want %q", out.String(), want)
	}
	if IsKittyProtocolActive() {
		t.Fatal("kitty protocol still marked active after disable")
	}
	out.Reset()
	term.disableKeyboardProtocol()
	if out.Len() != 0 {
		t.Fatalf("second disable wrote %q", out.String())
	}
}

// Draining while the terminal still reports key events never settles, so the
// protocol must be disabled first.
func TestDrainInputDisablesProtocolBeforeDraining(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	var out bytes.Buffer
	term := newProcessTerminal(r, nil, &out)
	resetKeyboardProtocolState(true, false, true)
	t.Cleanup(func() { resetKeyboardProtocolState(false, false, false) })
	if err := term.DrainInput(100*time.Millisecond, 20*time.Millisecond); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !strings.Contains(out.String(), keyboardProtocolPop) || keyboardProtocolPushed.Load() {
		t.Fatalf("DrainInput did not disable the protocol first; wrote %q", out.String())
	}
}

// With no captured cooked state the signal path must not write into a
// terminal it never took over.
func TestRestoreTerminalFromSignalWritesNothingWithoutState(t *testing.T) {
	var out bytes.Buffer
	saved := processTerminal
	processTerminal = newProcessTerminal(nil, nil, &out)
	t.Cleanup(func() { processTerminal = saved })
	savedState := cookedTerminalState.Load()
	cookedTerminalState.Store(nil)
	t.Cleanup(func() { cookedTerminalState.Store(savedState) })
	resetKeyboardProtocolState(true, false, true)
	t.Cleanup(func() { resetKeyboardProtocolState(false, false, false) })
	if RestoreTerminalFromSignal() || out.Len() != 0 {
		t.Fatalf("restored without state; wrote %q", out.String())
	}
}
