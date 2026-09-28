//go:build integration

// json_stream_test.go: integration coverage for `--mode json`, driven through
// the real wopr binary and the deterministic faux provider.
//
// These assert the properties a unit test cannot: that events reach a consumer
// *while the child is alive*, and that cancellation terminates promptly.

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// jsonModeCmd builds a `--mode json` invocation against the faux provider.
// woprHome is passed in rather than allocated per call: sessions live under
// WOPR_HOME, so a resume test must run both turns against the same home.
func jsonModeCmd(t *testing.T, woprHome string, extra ...string) *exec.Cmd {
	t.Helper()
	args := append([]string{
		"--mode", "json",
		"--model", "test-faux/echo",
	}, extra...)
	cmd := exec.Command(buildBinary(t), args...)
	cmd.Dir = woprHome
	cmd.Env = append(os.Environ(),
		"WOPR_HOME="+woprHome,
		"WOPR_CODING_AGENT_DIR="+filepath.Join(woprHome, "agent"),
		"WOPR_CODING_AGENT_SESSION_DIR=",
		"WOPR_OFFLINE=1",
		"WOPR_QUIET_STARTUP=1",
		"WOPR_TEST_FAUX=1",
	)
	return cmd
}

// decodeTypes reads JSONL and returns each object's event type.
func decodeType(t *testing.T, line string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("stdout line is not JSON: %q (%v)", line, err)
	}
	kind, _ := obj["type"].(string)
	return kind
}

// The whole point of the streaming contract: a consumer must be able to render
// progress from stdout while the child runs. Reading an event and asserting the
// process has not exited proves the output is not collected and dumped at exit,
// which is what wopr did before.
func TestJSONModeEmitsEventsBeforeExit(t *testing.T) {
	cmd := jsonModeCmd(t, t.TempDir(), "Run: sleep for a while")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// The faux "sleep" tool keeps the turn open, so the process is still
	// running while these early events arrive.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	var seen []string
	deadline := time.Now().Add(45 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		seen = append(seen, decodeType(t, scanner.Text()))
		if len(seen) >= 3 {
			break
		}
	}
	if len(seen) < 3 {
		t.Fatalf("read only %d events before the deadline: %v", len(seen), seen)
	}

	// Still alive: Signal(0) reports an error once the process is gone.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process already exited, so events were not streamed live: %v", err)
	}
	if seen[0] != "session" {
		t.Fatalf("first line = %q, want the session header", seen[0])
	}
}

// Cancellation must terminate both a backpressured JSON writer and an active
// tool with a draining consumer. The signal path disposes the runtime without
// awaiting the stdout flush, then exits with 128+signum.
func TestJSONModeCancellationTerminatesPromptly(t *testing.T) {
	for _, drain := range []bool{false, true} {
		name := "blocked-stdout"
		if drain {
			name = "draining-stdout"
		}
		t.Run(name, func(t *testing.T) {
			woprHome := t.TempDir()
			args := []string{}
			if !drain {
				promptPath := filepath.Join(woprHome, "system.txt")
				// Exceed pipe capacity so an unread consumer exercises backpressure.
				if err := os.WriteFile(promptPath, []byte(strings.Repeat("cancellation backpressure fixture\n", 1<<16)), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--system-prompt", promptPath)
			}
			cmd := jsonModeCmd(t, woprHome, append(args, "Run: sleep for a while")...)
			// Own the pipe separately from exec.Cmd: Wait must not close a pipe
			// that the consumer is still draining during process shutdown.
			stdout, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdout.Close() }()
			cmd.Stdout = writer
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				_ = writer.Close()
				t.Fatal(err)
			}
			_ = writer.Close()
			waitDone := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = cmd.Wait()
				close(waitDone)
			}()
			readerDone := make(chan struct{})
			started := make(chan struct{})
			var readErr error
			go func() {
				defer close(readerDone)
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					var event struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
						readErr = err
						return
					}
					if event.Type == "agent_start" {
						close(started)
						if drain {
							_, readErr = io.Copy(io.Discard, stdout)
						}
						return
					}
				}
				readErr = scanner.Err()
				if readErr == nil {
					readErr = io.EOF
				}
				readErr = fmt.Errorf("stream ended before agent_start: %w", readErr)
			}()
			// One waiter owns the child. Cleanup always joins it and the reader,
			// including readiness failures; no child or goroutine escapes a test.
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-waitDone
				_ = stdout.Close()
				<-readerDone
			})
			select {
			case <-started:
			case <-readerDone:
				if readErr != nil {
					t.Fatal(readErr)
				}
			case <-time.After(45 * time.Second):
				t.Fatal("stream did not emit agent_start")
			}
			if drain {
				// agent_start alone does not prove a tool is in flight. The faux
				// bash command writes this marker immediately before sleeping.
				marker := filepath.Join(woprHome, ai.TestFauxToolStartedMarker)
				if !pollUntil(45*time.Second, func() bool {
					_, err := os.Stat(marker)
					return err == nil
				}) {
					t.Fatal("faux tool never started")
				}
			}
			deadline := time.NewTimer(30 * time.Second)
			defer deadline.Stop()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-waitDone:
				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 128+int(syscall.SIGTERM) {
					t.Fatalf("wait = %v, want SIGTERM exit code %d; stderr: %s", waitErr, 128+int(syscall.SIGTERM), stderr.String())
				}
			case <-deadline.C:
				t.Fatal("child did not exit within 30s of SIGTERM")
			}
			select {
			case <-readerDone:
				if readErr != nil {
					t.Fatalf("stdout reader: %v", readErr)
				}
			case <-deadline.C:
				t.Fatal("stdout reader did not finish within 30s of SIGTERM")
			}
		})
	}
}
