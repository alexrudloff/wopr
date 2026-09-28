package efficiency

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/testenv"
)

// ─── ObservationPack ─────────────────────────────────────────────────────────

func bigText(lines int) string {
	var b strings.Builder
	for range lines {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 60))
		b.WriteString("\n")
	}
	return b.String()
}

func toolResult(id, text string) agent.AgentMessage {
	return agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Role: "toolResult", ToolCallID: id, ToolName: "bash", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: text}}}}
}

func assistant() agent.AgentMessage {
	return agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: "assistant"}}
}

func TestPackKeepsFirstTwoRequestsFullThenStablePlaceholder(t *testing.T) {
	root := t.TempDir()
	pack := NewPack(root)
	body := bigText(400) // ~26 KB
	var notified []string
	pack.Notify = func(m, s string, _ int) { notified = append(notified, m+": "+s) }

	messages := []agent.AgentMessage{toolResult("call-1", body)}
	// Request 1 and 2: full.
	for i := range FullSends {
		out := pack.Project(messages)
		if out[0].ToolResult.Text() != body {
			t.Fatalf("request %d should carry the full result", i+1)
		}
		messages = append(messages, assistant())
	}
	// Request 3: placeholder.
	out := pack.Project(messages)
	placeholder := out[0].ToolResult.Text()
	if !strings.Contains(placeholder, "obs_recall") || len(placeholder) >= len(body) {
		t.Fatalf("expected a placeholder, got %d bytes", len(placeholder))
	}
	if messages[0].ToolResult.Text() != body {
		t.Fatal("projection must not mutate the stored message")
	}
	// Request 4: identical placeholder (stable prefix for the cache).
	messages = append(messages, assistant())
	if again := pack.Project(messages)[0].ToolResult.Text(); again != placeholder {
		t.Fatal("placeholder must be stable across requests")
	}
	if len(notified) != 1 {
		t.Fatalf("expected one savings notice, got %v", notified)
	}
}

func TestRecallReturnsExactBytesAcrossPages(t *testing.T) {
	root := t.TempDir()
	pack := NewPack(root)
	body := bigText(900) // ~58 KB, several pages
	message := toolResult("call-2", body)
	observation := pack.NewObservation(message.ToolResult)
	if err := EnsureStored(observation); err != nil {
		t.Fatal(err)
	}
	tool := NewRecallTool(pack)
	var rebuilt strings.Builder
	offset := 0
	for range 20 {
		args, _ := json.Marshal(map[string]any{"id": observation.ID, "offset": offset})
		result, err := tool.Execute(context.Background(), "r", args, nil)
		if err != nil {
			t.Fatal(err)
		}
		header, text, _ := strings.Cut(result.Content, "\n")
		_, text, _ = strings.Cut(text, "\n")
		rebuilt.WriteString(text)
		if strings.Contains(header, "eof=true") {
			break
		}
		offset = result.Details.(map[string]any)["nextOffset"].(int)
	}
	if rebuilt.String() != body {
		t.Fatalf("recall did not reproduce the original (%d vs %d bytes)", rebuilt.Len(), len(body))
	}
}

func TestStorageFailsClosedOnMismatchAndSymlink(t *testing.T) {
	root := t.TempDir()
	pack := NewPack(root)
	observation := pack.NewObservation(toolResult("x", bigText(400)).ToolResult)
	if err := os.MkdirAll(filepath.Dir(observation.FilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Same size, different content.
	if err := os.WriteFile(observation.FilePath, []byte(strings.Repeat("y", observation.Bytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStored(observation); err == nil {
		t.Fatal("expected a hash mismatch error")
	}
	// Symlinked object refuses recall.
	_ = os.Remove(observation.FilePath)
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	_ = os.WriteFile(target, []byte(observation.Text), 0o600)
	testenv.Symlink(t, target, observation.FilePath)
	if _, err := ReadRecallChunk(observation.FilePath, 0, 1000, 10); err == nil {
		t.Fatal("expected recall through a symlink to fail")
	}
}

type fakeWrite struct {
	dir  string
	fail bool
}

func (f *fakeWrite) Name() string { return "write" }

func (f *fakeWrite) Label() string { return "" }

func (f *fakeWrite) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (f *fakeWrite) Schema() ai.ToolSchema {
	return ai.ToolSchema{Name: "write", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}}
}

func (f *fakeWrite) Execute(_ context.Context, _ string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct{ Path, Content string }
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	if strings.Contains(string(params), "then_run") {
		return agent.AgentToolResult{}, errors.New("then_run leaked into the base tool")
	}
	if f.fail {
		return agent.AgentToolResult{Content: "write failed", IsError: true}, nil
	}
	if err := os.WriteFile(filepath.Join(f.dir, in.Path), []byte(in.Content), 0o600); err != nil {
		return agent.AgentToolResult{}, err
	}
	return agent.AgentToolResult{Content: "Successfully wrote to " + in.Path}, nil
}

type fakeBash struct {
	dir     string
	calls   []string
	failure bool
}

func (b *fakeBash) Name() string { return "bash" }

func (b *fakeBash) Label() string { return "" }

func (b *fakeBash) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (b *fakeBash) Schema() ai.ToolSchema { return ai.ToolSchema{Name: "bash"} }

func (b *fakeBash) Execute(_ context.Context, _ string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct{ Command string }
	_ = json.Unmarshal(params, &in)
	b.calls = append(b.calls, in.Command)
	data, _ := os.ReadFile(filepath.Join(b.dir, "a.txt"))
	if b.failure {
		return agent.AgentToolResult{Content: "boom\n\nCommand exited with code 1", IsError: true}, nil
	}
	return agent.AgentToolResult{Content: "saw:" + string(data)}, nil
}

func TestFusedSkipsCommandWhenMutationFails(t *testing.T) {
	dir := t.TempDir()
	bash := &fakeBash{dir: dir}
	fused := Fuse(&fakeWrite{dir: dir, fail: true}, bash, dir)
	result, _ := fused.Execute(context.Background(), "c2", json.RawMessage(`{"path":"a.txt","content":"x","then_run":{"command":"make"}}`), nil)
	if !result.IsError || !strings.Contains(result.Content, ThenRunSkipped) || len(bash.calls) != 0 {
		t.Fatalf("expected skipped command, got %+v calls=%v", result, bash.calls)
	}
}

func failingLog() string {
	var b strings.Builder
	for range 200 {
		b.WriteString("=== RUN   TestSomething/case_")
		b.WriteString(strings.Repeat("a", 10))
		b.WriteString("\n--- PASS: TestSomething (0.00s)\n")
	}
	b.WriteString("--- FAIL: TestRouter (0.01s)\n    router_test.go:42: expected local, got cluster\nFAIL\n")
	return b.String()
}

func TestReducerAcceptsOnlyVerifiedQuotes(t *testing.T) {
	root := t.TempDir()
	body := failingLog()
	var hash string
	reducer := NewReducer(root, func(_ context.Context, _, user string, _ int, _ time.Duration) (ReducerResponse, error) {
		for line := range strings.SplitSeq(user, "\n") {
			if value, ok := strings.CutPrefix(line, "source_sha256="); ok {
				hash = value
			}
		}
		receipt, _ := json.Marshal(map[string]any{
			"schema": ReceiptSchema, "source_sha256": hash, "status": "failure", "uncertain": false,
			"evidence": []map[string]string{{"kind": "failure", "quote": "--- FAIL: TestRouter (0.01s)"}, {"kind": "fatal", "quote": "router_test.go:42: expected local, got cluster"}},
		})
		return ReducerResponse{Provider: "local", Model: "deepseek-v4-flash", Text: string(receipt), OK: true, StopReason: "stop", TotalTokens: 900}, nil
	})
	args := json.RawMessage(`{"command":"go test ./..."}`)
	reduced := reducer.Reduce(context.Background(), "t1", "bash", args, agent.AgentToolResult{Content: body, IsError: true})
	if reduced == nil {
		t.Fatal("expected a verified receipt")
	}
	if !strings.HasPrefix(reduced.Content, ReceiptPrefix) || !strings.Contains(reduced.Content, "line=402") {
		t.Fatalf("receipt missing prefix or line numbers:\n%s", reduced.Content)
	}
	if len(reduced.Content) >= len(body) {
		t.Fatal("receipt must be smaller than the log")
	}
}

func TestReducerRejectsHallucinatedQuote(t *testing.T) {
	root := t.TempDir()
	body := failingLog()
	reducer := NewReducer(root, func(_ context.Context, _, user string, _ int, _ time.Duration) (ReducerResponse, error) {
		hash := ""
		for line := range strings.SplitSeq(user, "\n") {
			if value, ok := strings.CutPrefix(line, "source_sha256="); ok {
				hash = value
			}
		}
		receipt, _ := json.Marshal(map[string]any{
			"schema": ReceiptSchema, "source_sha256": hash, "status": "failure", "uncertain": false,
			"evidence": []map[string]string{{"kind": "failure", "quote": "panic: nil map write in router.go"}},
		})
		return ReducerResponse{Text: string(receipt), OK: true}, nil
	})
	if reduced := reducer.Reduce(context.Background(), "t2", "bash", json.RawMessage(`{"command":"go test ./..."}`), agent.AgentToolResult{Content: body, IsError: true}); reduced != nil {
		t.Fatal("a quote not in the log must reject the receipt")
	}
}

type memStore struct{ states []OnlineState }

func (m *memStore) AppendState(s OnlineState) error { m.states = append(m.states, s); return nil }

func (m *memStore) LoadState() (OnlineState, bool) {
	if len(m.states) == 0 {
		return OnlineState{}, false
	}
	return m.states[len(m.states)-1], true
}

type fakeCompactor struct {
	tokens, window int
	feasible       bool
}

func (f fakeCompactor) ContextTokens() int { return f.tokens }

func (f fakeCompactor) SystemPromptTokens() int { return 2000 }

func (f fakeCompactor) ContextWindow() int { return f.window }

func (f fakeCompactor) NativeCompactionFeasible() bool { return f.feasible }

func (f fakeCompactor) KeepRecentTokens() int { return 20000 }

func (f fakeCompactor) OpenItems() int { return 0 }

func TestStateRestoresFromStore(t *testing.T) {
	store := &memStore{}
	m := NewManager(store, fakeCompactor{}, DefaultCacheWriteReadRatio)
	m.RecordBoundary("p1")
	again := NewManager(store, fakeCompactor{}, DefaultCacheWriteReadRatio)
	if len(again.State().CompletedBoundaryRequestCounts) != 1 {
		t.Fatal("state should restore from the store")
	}
}
