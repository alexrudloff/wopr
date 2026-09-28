package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/internal/testbudget"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type rpcRecord = map[string]any

// rpcProcess drives one `wopr --mode rpc` child over its JSONL stdin/stdout.
// Every wait synchronizes on the awaited stdout record; testbudget.Wait only
// bounds how long a hang takes to fail.
type rpcProcess struct {
	t          *testing.T
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stderr     *lockedBuffer
	records    chan rpcRecord
	stopOutput chan struct{}
	outputDone chan struct{}
	outputErr  error
	budget     time.Duration
	exited     bool
}

func startRPCProcessAt(t *testing.T, cwd string, env []string, args ...string) *rpcProcess {
	t.Helper()
	binary := woprBinary(t)
	cmd := exec.Command(binary, append([]string{"--mode", "rpc"}, args...)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &rpcProcess{
		t: t, cmd: cmd, stdin: stdin, stderr: &lockedBuffer{},
		records: make(chan rpcRecord, 64),
		budget:  testbudget.Wait(t),
	}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.scanOutput(stdout)
	t.Cleanup(func() {
		close(p.stopOutput)
		_ = stdout.Close()
		<-p.outputDone
		if p.exited {
			return
		}
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return p
}

func (p *rpcProcess) scanOutput(stdout io.Reader) {
	p.stopOutput = make(chan struct{})
	p.outputDone = make(chan struct{})
	go func() {
		defer close(p.outputDone)
		defer close(p.records)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var value rpcRecord
			if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
				p.outputErr = err
				return
			}
			select {
			case p.records <- value:
			case <-p.stopOutput:
				return
			}
		}
		p.outputErr = scanner.Err()
	}()
}

func (p *rpcProcess) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

// await consumes records until done reports true.
func (p *rpcProcess) await(what string, done func(rpcRecord) bool) {
	p.t.Helper()
	p.awaitProgress(func() string { return what }, done)
}

// awaitProgress is await with a description evaluated at failure time, so a
// timeout reports which of several awaited records arrived.
func (p *rpcProcess) awaitProgress(what func() string, done func(rpcRecord) bool) {
	p.t.Helper()
	timer := time.NewTimer(p.budget)
	defer timer.Stop()
	for {
		select {
		case record, ok := <-p.records:
			if !ok {
				p.t.Fatalf("RPC output ended before %s: %v\n%s", what(), p.outputErr, p.stderr.String())
			}
			if done(record) {
				return
			}
		case <-timer.C:
			p.t.Fatalf("RPC %s timed out after %s\n%s", what(), p.budget, p.stderr.String())
		}
	}
}

// closeInput sends EOF to the RPC process.
func (p *rpcProcess) closeInput() {
	p.t.Helper()
	if err := p.stdin.Close(); err != nil {
		p.t.Fatal(err)
	}
}

// waitForExit requires a clean exit after stdin closes. A process that never
// exits is killed and fails the test once the wait budget expires.
func (p *rpcProcess) waitForExit(what string) {
	p.t.Helper()
	waited := make(chan error, 1)
	go func() { waited <- p.cmd.Wait() }()
	timer := time.NewTimer(p.budget)
	defer timer.Stop()
	select {
	case err := <-waited:
		p.exited = true
		if err != nil {
			p.t.Fatalf("RPC process: %v\n%s", err, p.stderr.String())
		}
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-waited
		p.exited = true
		p.t.Fatalf("RPC process did not stop after EOF %s within %s\n%s", what, p.budget, p.stderr.String())
	}
}

// closeAndWait sends EOF and requires a clean exit.
func (p *rpcProcess) closeAndWait(what string) {
	p.t.Helper()
	p.closeInput()
	p.waitForExit(what)
}

func isSuccessResponse(record rpcRecord, id string) bool {
	return record["type"] == "response" && record["id"] == id && record["success"] == true
}

// TestRPCContract drives `wopr --mode rpc` over JSONL: a prompt round trip
// (which writes no metadata side files, GUARD-19), get_messages and
// get_state, a bash command, and a malformed line.
func TestRPCContract(t *testing.T) {
	home := t.TempDir()
	p := startRPCProcessAt(t, t.TempDir(), []string{
		"HOME=" + home, "WOPR_HOME=" + home, "WOPR_CODING_AGENT_DIR=" + filepath.Join(home, "agent"),
		"WOPR_TEST_FAUX=1",
	}, "--model", "test-faux/faux-1", "--no-session")

	p.send(`{"id":"p","type":"prompt","message":"reply with exactly: meta","metadata":{"task_id":"t"}}`)
	p.await("prompt settling", func(r rpcRecord) bool { return r["type"] == "agent_settled" })
	for _, path := range []string{
		fmt.Sprintf("/tmp/.wopr-current-task-id.%d", p.cmd.Process.Pid),
		fmt.Sprintf("/tmp/.wopr-current-prompt-metadata.%d", p.cmd.Process.Pid),
	} {
		if _, err := os.Stat(path); err == nil {
			_ = os.Remove(path)
			t.Errorf("RPC prompt wrote side file %s", path)
		}
	}

	p.send(`{"id":"m","type":"get_messages"}`)
	p.await("get_messages", func(r rpcRecord) bool {
		if !isSuccessResponse(r, "m") {
			return false
		}
		messages, _ := r["data"].(map[string]any)["messages"].([]any)
		if len(messages) < 2 {
			t.Fatalf("messages = %#v", messages)
		}
		user, reply := messages[len(messages)-2].(map[string]any), messages[len(messages)-1].(map[string]any)
		if user["role"] != "user" || reply["role"] != "assistant" || !strings.Contains(fmt.Sprint(reply["content"]), "meta") {
			t.Fatalf("last exchange = %#v, %#v", user, reply)
		}
		return true
	})
	p.send(`{"id":"s","type":"get_state"}`)
	p.await("get_state", func(r rpcRecord) bool {
		if !isSuccessResponse(r, "s") {
			return false
		}
		data := r["data"].(map[string]any)
		model, _ := data["model"].(map[string]any)
		if model["provider"] != "test-faux" || data["messageCount"] == float64(0) || data["isStreaming"] != false {
			t.Fatalf("state = %#v", data)
		}
		return true
	})

	p.send(`{"id":"b","type":"bash","command":"echo hi"}`)
	p.await("bash", func(r rpcRecord) bool {
		if !isSuccessResponse(r, "b") {
			return false
		}
		data := r["data"].(map[string]any)
		if data["exitCode"] != float64(0) || !strings.Contains(fmt.Sprint(data["output"]), "hi") {
			t.Fatalf("bash = %#v", data)
		}
		return true
	})

	p.send("not json")
	p.await("parse error", func(r rpcRecord) bool {
		return r["type"] == "response" && r["command"] == "parse" && r["success"] == false
	})
	p.closeAndWait("after the contract run")
}
