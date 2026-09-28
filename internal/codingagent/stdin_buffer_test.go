package codingagent

import (
	"reflect"
	"testing"
)

func TestStdinBuffer_PartialCSISequenceAcrossReads(t *testing.T) {
	var b StdinBuffer
	if got := b.ProcessString("\x1b"); len(got) != 0 {
		t.Fatalf("first chunk = %#v want nil", got)
	}
	if !b.HasPendingFlush() {
		t.Fatal("expected pending flush after partial ESC")
	}
	got := b.ProcessString("[A")
	want := []string{"\x1b[A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completed CSI = %#v want %#v", got, want)
	}
	if b.HasPendingFlush() {
		t.Fatal("unexpected pending flush after complete CSI")
	}
}

func TestStdinBuffer_FlushesIncompleteEscapeRemainder(t *testing.T) {
	var b StdinBuffer
	if got := b.ProcessString("\x1b["); len(got) != 0 {
		t.Fatalf("partial CSI = %#v want nil", got)
	}
	got := b.Flush()
	want := []string{"\x1b["}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Flush() = %#v want %#v", got, want)
	}
	if b.HasPendingFlush() {
		t.Fatal("buffer should be empty after flush")
	}
}

func TestStdinBuffer_BracketedPasteAcrossReads(t *testing.T) {
	var b StdinBuffer
	var got []string
	got = append(got, b.ProcessString("ab")...)
	got = append(got, b.ProcessString("\x1b[200~hello")...)
	got = append(got, b.ProcessString("\nworld\x1b[201~z")...)
	want := []string{"a", "b", bracketedPasteStart + "hello\nworld" + bracketedPasteEnd, "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v want %#v", got, want)
	}
}

// Regular input is emitted one character at a time, not one byte at a time.
func TestStdinBuffer_UnicodeCharacters(t *testing.T) {
	var b StdinBuffer
	got := b.ProcessString("hello 世界")
	want := []string{"h", "e", "l", "l", "o", " ", "世", "界"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProcessString() = %q want %q", got, want)
	}
}
