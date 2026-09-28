//go:build unix

package tui

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Negotiation replies are consumed as they arrive, so Stop does not wait for
// an unrelated key, and the next key is left for the next terminal owner.
func TestStopAfterNegotiationOnlyInput(t *testing.T) {
	for _, reply := range []string{"\x1b[?62;22c", "\x1b[?0u\x1b[?62;22c"} {
		t.Run(reply, func(t *testing.T) {
			preserveKeyboardProtocolState(t)
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			output := &negotiationWriter{seen: make(chan struct{})}
			terminal := newProcessTerminal(r, nil, output)
			ctx, cancel := context.WithCancel(t.Context())
			terminal.stopReader = cancel
			terminal.readerDone = make(chan struct{})
			done := terminal.readerDone
			inputs := make(chan string, 1)
			go func() {
				defer close(done)
				terminal.forwardInput(ctx, func(data []byte) { inputs <- string(data) }, nil)
			}()
			_, _ = w.WriteString(reply)
			select {
			case <-output.seen:
			case <-time.After(5 * time.Second):
				t.Fatal("negotiation did not reach the reader")
			}
			stopped := make(chan struct{})
			go func() { terminal.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				_, _ = w.WriteString("x")
				<-stopped
				t.Fatal("Stop blocked after negotiation-only input")
			}
			select {
			case data := <-inputs:
				t.Fatalf("negotiation delivered as input: %q", data)
			default:
			}
			_, _ = w.WriteString("x")
			if data, err := ReadInput(r); err != nil || string(data) != "x" {
				t.Fatalf("next owner read %q, %v; want x", data, err)
			}
		})
	}
}

// A stopped startup terminal must leave later stdin bytes for the interactive
// reader: stopping removes the listener without draining stdin.
func TestStoppedTerminalReaderDoesNotEatNextKeystroke(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	terminal := newProcessTerminal(r, nil, &strings.Builder{})
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan []byte, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		terminal.forwardInput(ctx, func(data []byte) { got <- data }, nil)
	}()
	_, _ = w.Write([]byte("\r"))
	if data := <-got; string(data) != "\r" {
		t.Fatalf("startup reader got %q, want Enter", data)
	}
	for deadline := time.Now().Add(5 * time.Second); ; runtime.Gosched() {
		stack := make([]byte, 1<<20)
		if strings.Contains(string(stack[:runtime.Stack(stack, true)]), "unix.Poll") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup reader did not wait for its next input")
		}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopped terminal left a reader blocked in stdin.Read")
	}
	mainInput := make(chan []byte, 1)
	go func() {
		if data, err := ReadInput(r); err == nil {
			mainInput <- data
		}
	}()
	_, _ = w.Write([]byte("/"))
	select {
	case data := <-mainInput:
		if string(data) != "/" {
			t.Fatalf("first keystroke after startup prompt = %q, want /", data)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive reader did not receive the first post-startup key")
	}
}

// Every byte forwardInput already read must be delivered, never dropped when a
// cancel races a keystroke burst.
func TestForwardInputDoesNotDropReadBytesOnCancel(t *testing.T) {
	payload := []byte("0123456789/settings")
	for iter := range 200 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		terminal := newProcessTerminal(r, nil, &strings.Builder{})
		ctx, cancel := context.WithCancel(context.Background())
		var mu sync.Mutex
		var delivered []byte
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			terminal.forwardInput(ctx, func(data []byte) {
				mu.Lock()
				delivered = append(delivered, data...)
				mu.Unlock()
			}, nil)
		}()
		rng := rand.New(rand.NewSource(int64(iter)))
		for _, b := range payload {
			_, _ = w.Write([]byte{b})
			if rng.Intn(3) == 0 {
				time.Sleep(time.Duration(rng.Intn(200)) * time.Microsecond)
			}
		}
		time.Sleep(time.Duration(rng.Intn(300)) * time.Microsecond)
		cancel()
		<-readerDone
		_ = w.Close()
		rest, _ := io.ReadAll(r)
		_ = r.Close()
		mu.Lock()
		total := append(append([]byte(nil), delivered...), rest...)
		mu.Unlock()
		if !bytes.Equal(total, payload) {
			t.Fatalf("iteration %d: delivered+unread = %q, want %q", iter, total, payload)
		}
	}
}
