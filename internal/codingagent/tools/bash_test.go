package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
)

// bashParams marshals shell tool input in tests; a zero timeout is omitted.
type bashParams struct {
	Command string  `json:"command"`
	Timeout float64 `json:"timeout,omitempty"`
}

// A grandchild holding the stdout pipe must not hang the call after cancel:
// the whole process group is killed.
func TestBashKillsProcessGroupOnCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	bt := &BashTool{CWD: t.TempDir()}

	// Long-running grandchild that would normally hold the pipe open.
	args, _ := json.Marshal(bashParams{Command: "sleep 30 &\necho started\nwait"})

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan agent.AgentToolResult, 1)
	go func() {
		res, _ := bt.Execute(ctx, "", args, nil)
		resultCh <- res
	}()

	// Give bash time to start.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case res := <-resultCh:
		if !res.IsError {
			t.Errorf("expected IsError after cancel, got success: %s", res.Content)
		}
		if !strings.Contains(res.Content, "aborted") {
			t.Errorf("expected abort message, got: %s", res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bash didn't return within 5s after cancel \u2014 process group kill is broken (children still hold pipes)")
	}
}

func TestBashTimeoutMessage(t *testing.T) {
	bt := &BashTool{CWD: t.TempDir()}
	args, _ := json.Marshal(bashParams{Command: "sleep 5", Timeout: 0.5})
	ctx := t.Context()
	start := time.Now()
	res, _ := bt.Execute(ctx, "", args, nil)
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Errorf("timeout=0.5 should return in well under 5s, took %v", elapsed)
	}
	if !res.IsError {
		t.Error("timeout should produce IsError=true")
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Errorf("missing timeout message in: %q", res.Content)
	}
}

func TestBashTempFileOverflow(t *testing.T) {
	// Produce 60 KB of output (above DEFAULT_MAX_BYTES = 50 KB) so the
	// tempfile path triggers and a truncation warning is emitted.
	bt := &BashTool{CWD: t.TempDir()}
	// ~3000 lines of "x" each → bytes overflow first.
	args, _ := json.Marshal(bashParams{Command: `awk 'BEGIN{for(i=1;i<=3000;i++) printf "%020d\n", i}'`})
	res, _ := bt.Execute(context.Background(), "", args, nil)

	d, ok := res.Details.(*BashDetails)
	if !ok || d == nil {
		t.Fatalf("expected *BashDetails on truncated output, got %T (%+v)", res.Details, res.Details)
	}
	if d.FullOutputPath == "" {
		t.Errorf("expected full output path to be set")
	}
	// Tempfile should be readable and contain the original full output.
	full, err := os.ReadFile(d.FullOutputPath)
	if err != nil {
		t.Fatalf("read tempfile %s: %v", d.FullOutputPath, err)
	}
	if len(full) < 50_000 {
		t.Errorf("tempfile too small (%d bytes); should hold full untruncated output", len(full))
	}
	if !strings.Contains(res.Content, "[Showing lines") {
		t.Errorf("expected '[Showing lines ...]' annotation in content; got %q", res.Content[:min(200, len(res.Content))])
	}
	if !strings.Contains(res.Content, d.FullOutputPath) {
		t.Errorf("annotation should mention tempfile path %s; content=%q",
			d.FullOutputPath, res.Content[max(0, len(res.Content)-300):])
	}
}

func TestBashAbortReturnsBufferedOutput(t *testing.T) {
	bt := &BashTool{CWD: t.TempDir()}
	args, _ := json.Marshal(bashParams{Command: "echo started\nsleep 30"})
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan agent.AgentToolResult, 1)
	started := make(chan struct{})
	var startedOnce sync.Once
	go func() {
		res, _ := bt.Execute(ctx, "", args, func(content string, _ any) {
			if strings.Contains(content, "started") {
				startedOnce.Do(func() { close(started) })
			}
		})
		resultCh <- res
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bash command did not produce its pre-abort output")
	}
	cancel()
	select {
	case res := <-resultCh:
		if !res.IsError {
			t.Errorf("expected IsError after abort")
		}
		if !strings.Contains(res.Content, "started") {
			t.Errorf("buffered output 'started' must survive abort; got %q", res.Content)
		}
		if !strings.Contains(res.Content, "Command aborted") {
			t.Errorf("expected 'Command aborted' message; got %q", res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abort didn't return within 5s")
	}
}

// A background child that inherits stdout must not turn the call into an
// error or discard the output: the call resolves once the pipe has been idle
// for exitStdioGrace after the shell exits.
func TestBashTool_BackgroundChildKeepsResult(t *testing.T) {
	bt := &BashTool{CWD: t.TempDir()}
	args, _ := json.Marshal(bashParams{Command: "echo hi; sleep 30 &"})
	start := time.Now()
	res, err := bt.Execute(context.Background(), "", args, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || strings.TrimSpace(res.Content) != "hi" {
		t.Fatalf("res = %+v, want success with output hi", res)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("call took %v; it must not wait for the background child", elapsed)
	}
}
