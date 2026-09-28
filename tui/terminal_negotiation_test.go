package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

type negotiationWriter struct {
	seen chan struct{}
	once sync.Once
}

func (w *negotiationWriter) Write(p []byte) (int, error) {
	if string(p) == modifyOtherKeysEnable {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func preserveKeyboardProtocolState(t *testing.T) {
	t.Helper()
	kitty, modify, pushed := kittyProtocolActive.Load(), modifyOtherKeysActive.Load(), keyboardProtocolPushed.Load()
	kittyProtocolActive.Store(false)
	modifyOtherKeysActive.Store(false)
	keyboardProtocolPushed.Store(false)
	t.Cleanup(func() {
		kittyProtocolActive.Store(kitty)
		modifyOtherKeysActive.Store(modify)
		keyboardProtocolPushed.Store(pushed)
	})
}

// A DA reply without a Kitty reply falls back to modifyOtherKeys; a Kitty
// reply wins and turns the fallback off again.
func TestKeyboardProtocolNegotiation(t *testing.T) {
	preserveKeyboardProtocolState(t)
	var output bytes.Buffer
	terminal := newProcessTerminal(nil, nil, &output)
	if !terminal.handleKeyboardProtocolNegotiationSequence("\x1b[?1;2c") || !modifyOtherKeysActive.Load() {
		t.Fatal("device attributes reply did not enable the modifyOtherKeys fallback")
	}
	output.Reset()
	if !terminal.handleKeyboardProtocolNegotiationSequence("\x1b[?7u") || !IsKittyProtocolActive() || modifyOtherKeysActive.Load() {
		t.Fatal("kitty reply did not take over from modifyOtherKeys")
	}
	if !strings.Contains(output.String(), "\x1b[>4;0m") {
		t.Fatalf("kitty confirmation did not disable modifyOtherKeys: %q", output.String())
	}
	if terminal.handleKeyboardProtocolNegotiationSequence("\x1b[A") {
		t.Fatal("ordinary arrow key consumed as negotiation")
	}
	// Windows Terminal frames every byte as a win32 record under DECSET 9001,
	// which wopr cannot decode.
	if strings.Contains(extendedKeyInit, "9001") {
		t.Fatalf("extendedKeyInit enables win32-input-mode: %q", extendedKeyInit)
	}
}

// Negotiation replies must never reach a focused component as typed input.
func TestReadInputStripsNegotiationResponses(t *testing.T) {
	preserveKeyboardProtocolState(t)
	for _, write := range []string{"\x1b[?7u\x1b[?62;22ca", "\x1b[?62;22ca"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		term := newProcessTerminal(r, nil, ioDiscard{})
		_, _ = w.Write([]byte(write))
		got, err := term.readInput(r)
		_ = r.Close()
		_ = w.Close()
		if err != nil || string(got) != "a" {
			t.Fatalf("readInput(%q) = %q, %v; want \"a\"", write, got, err)
		}
	}
}

func TestReadInputWaitsPastNegotiationOnlyRead(t *testing.T) {
	preserveKeyboardProtocolState(t)
	terminal := newProcessTerminal(nil, nil, &negotiationWriter{seen: make(chan struct{})})
	r := &separateInputReads{chunks: []string{"\x1b[?0u", "\x1b[?62;22c", "x"}}
	got, err := terminal.readInput(r)
	if err != nil || string(got) != "x" || len(r.chunks) != 0 {
		t.Fatalf("readInput = %q, %v; %d unread chunks", got, err, len(r.chunks))
	}
}

type separateInputReads struct{ chunks []string }

func (r *separateInputReads) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}
