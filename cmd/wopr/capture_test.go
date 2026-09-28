package main

import (
	"io"
	"os"
	"testing"
)

type pipeReadResult struct {
	data []byte
	err  error
}

func drainPipe(r *os.File) <-chan pipeReadResult {
	done := make(chan pipeReadResult, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- pipeReadResult{data: data, err: err}
	}()
	return done
}

func captureStdoutStderr(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	outDone := drainPipe(rOut)
	errDone := drainPipe(rErr)
	code = fn()
	_ = wOut.Close()
	_ = wErr.Close()
	out := <-outDone
	if out.err != nil {
		t.Fatal(out.err)
	}
	errOutput := <-errDone
	if errOutput.err != nil {
		t.Fatal(errOutput.err)
	}
	_ = rOut.Close()
	_ = rErr.Close()
	return string(out.data), string(errOutput.data), code
}
