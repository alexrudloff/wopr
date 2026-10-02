package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// A pipe that stays open and silent must not hang a run that already has a
// prompt, and cancelling (SIGTERM) must stop a read that waits on stdin.
func TestPipedStdinNeverHangs(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	old, oldWait := os.Stdin, stdinWait
	os.Stdin, stdinWait = r, 50*time.Millisecond
	defer func() { os.Stdin, stdinWait = old, oldWait }()

	done := make(chan struct{})
	go func() {
		if got, args := readPipedStdin(context.Background(), []string{"hi"}); got != "" || len(args) != 1 {
			t.Errorf("silent pipe = %q %q", got, args)
		}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		if got, args := readPipedStdin(ctx, []string{"-"}); got != "" || len(args) != 0 {
			t.Errorf("cancelled read = %q %q", got, args)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading stdin hung")
	}
}
