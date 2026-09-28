package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestReadInputSwallowsKittyProtocolResponse(t *testing.T) {
	SetKittyProtocolActive(false)
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	go func() { _, _ = w.Write([]byte("\x1b[?7ua")) }()
	got, err := ReadInput(r)
	if err != nil || string(got) != "a" {
		t.Fatalf("ReadInput = %q, %v; want \"a\"", got, err)
	}
	if !IsKittyProtocolActive() {
		t.Fatal("kitty protocol not marked active after response")
	}
}

func TestProcessTerminalForwardInputReportsEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	term := newProcessTerminal(r, nil, ioDiscard{})
	inputs := make(chan []byte, 1)
	readErrors := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go term.forwardInput(ctx, func(data []byte) { inputs <- append([]byte(nil), data...) }, func(err error) { readErrors <- err })
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	select {
	case data := <-inputs:
		if string(data) != "x" {
			t.Fatalf("input = %q, want x", data)
		}
	case <-time.After(time.Second):
		t.Fatal("input callback did not run")
	}
	select {
	case err := <-readErrors:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read error = %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF callback did not run")
	}
}

func TestProcessTerminalStartFailureAndStopAreSafe(t *testing.T) {
	term := newProcessTerminal(nil, nil, ioDiscard{})
	if err := term.Start(nil, nil); err == nil {
		t.Fatal("Start without a TTY succeeded")
	}
	term.Stop()
	term.Stop()
}
