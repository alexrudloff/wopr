package codingagent

import (
	"errors"
	"strings"
	"testing"
)

func newTestSlashContext() (*SlashContext, *strings.Builder, *bool, *bool) {
	var out strings.Builder
	var cleared, quit bool
	sc := &SlashContext{
		Append:     func(s string) { out.WriteString(s); out.WriteString("\n") },
		AppendText: func(s string) { out.WriteString(s); out.WriteString("\n") },
		Clear:      func() { cleared = true },
		Quit:       func() { quit = true },
		Reset:      func() {},
	}
	return sc, &out, &cleared, &quit
}

func TestSlashRegistryResolves(t *testing.T) {
	r := NewSlashRegistry()
	_, ok := r.Resolve("quit")
	if !ok {
		t.Fatal("/quit did not resolve")
	}
	// /exit was removed. Verify it's unknown.
	_, ok = r.Resolve("exit")
	if ok {
		t.Fatal("/exit should not resolve (removed)")
	}
}

func TestSlashRegistryUnknownCommand(t *testing.T) {
	r := NewSlashRegistry()
	sc, _, _, _ := newTestSlashContext()
	err := r.Dispatch(sc, "/nope")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !errors.Is(err, ErrUnknownSlashCommand) {
		t.Fatalf("err = %v, want ErrUnknownSlashCommand", err)
	}
}
