package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// ─── Fake helpers ─────────────────────────────────────────────────────────────

func agentTestStream(events []ai.AssistantMessageEvent) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	for _, event := range events {
		if err := stream.Push(event); err != nil {
			panic(err)
		}
	}
	return stream
}

func agentTestAssistant(content []ai.AssistantContentBlock, reason ai.StopReason) *ai.AssistantMessage {
	return &ai.AssistantMessage{Content: content, Provider: "test", Model: "fake", StopReason: reason}
}

// staticProvider replays a fixed event sequence on every Stream call.
type staticProvider struct {
	events []ai.AssistantMessageEvent
}

func (p *staticProvider) ID() string   { return "static-fake" }
func (p *staticProvider) Close() error { return nil }
func (p *staticProvider) Stream(_ context.Context, _ ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	return agentTestStream(p.events), nil
}

// sequencedProvider returns a different event sequence on each successive call.
// When all sequences are exhausted it returns an empty stream.
type sequencedProvider struct {
	seqs [][]ai.AssistantMessageEvent
	idx  atomic.Int32
}

func (p *sequencedProvider) ID() string   { return "seq-fake" }
func (p *sequencedProvider) Close() error { return nil }
func (p *sequencedProvider) Stream(_ context.Context, _ ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	i := int(p.idx.Add(1)) - 1
	var events []ai.AssistantMessageEvent
	if i < len(p.seqs) {
		events = p.seqs[i]
	} else {
		events = textSeq("")
	}
	return agentTestStream(events), nil
}

func providerFromSeqs(seqs ...[]ai.AssistantMessageEvent) *sequencedProvider {
	return &sequencedProvider{seqs: seqs}
}

// toolCallSeq builds a stream sequence that emits tool calls followed by done.
func toolCallSeq(calls ...struct{ id, name string }) []ai.AssistantMessageEvent {
	content := make([]ai.AssistantContentBlock, 0, len(calls))
	start := agentTestAssistant(nil, ai.StopReasonPending)
	events := []ai.AssistantMessageEvent{ai.StartEvent{Partial: start}}
	for _, call := range calls {
		index := len(content)
		toolCall := ai.ToolCall{ID: call.id, Name: call.name, Arguments: ai.JsonObject{}}
		content = append(content, toolCall)
		partial := agentTestAssistant(append([]ai.AssistantContentBlock(nil), content...), ai.StopReasonPending)
		events = append(events,
			ai.ToolCallStartEvent{ContentIndex: index, Partial: partial},
			ai.ToolCallDeltaEvent{ContentIndex: index, Delta: `{}`, Partial: partial},
			ai.ToolCallEndEvent{ContentIndex: index, ToolCall: toolCall, Partial: partial},
		)
	}
	final := agentTestAssistant(content, ai.StopReasonToolUse)
	return append(events, ai.DoneEvent{Reason: ai.StopReasonToolUse, Message: final})
}

// textSeq builds a stream sequence that emits text + done (no tools).
func textSeq(text string) []ai.AssistantMessageEvent {
	start := agentTestAssistant(nil, ai.StopReasonPending)
	content := []ai.AssistantContentBlock(nil)
	events := []ai.AssistantMessageEvent{ai.StartEvent{Partial: start}}
	if text != "" {
		content = []ai.AssistantContentBlock{ai.TextContent{Text: text}}
		partial := agentTestAssistant(content, ai.StopReasonPending)
		events = append(events,
			ai.TextStartEvent{ContentIndex: 0, Partial: partial},
			ai.TextDeltaEvent{ContentIndex: 0, Delta: text, Partial: partial},
			ai.TextEndEvent{ContentIndex: 0, Content: text, Partial: partial},
		)
	}
	final := agentTestAssistant(content, ai.StopReasonStop)
	return append(events, ai.DoneEvent{Reason: ai.StopReasonStop, Message: final})
}

func fakeTestModel(prov ai.Provider) *ai.Model {
	return &ai.Model{
		ID:           "fake",
		DisplayName:  "fake",
		Provider:     prov,
		Capabilities: ai.ModelCapabilities{ContextWindow: 8000},
	}
}

// fakeTool is an AgentTool for unit tests.
type fakeTool struct {
	name    string
	mode    ToolExecutionMode
	delay   time.Duration
	content string
	isError bool
	execErr error          // non-nil = Execute returns a Go error (tool threw)
	params  map[string]any // non-nil = JSON schema enforced before Execute
}

func (t *fakeTool) Name() string                     { return t.name }
func (t *fakeTool) Label() string                    { return "" }
func (t *fakeTool) Description() string              { return t.name }
func (t *fakeTool) Schema() ai.ToolSchema            { return ai.ToolSchema{Name: t.name, Parameters: t.params} }
func (t *fakeTool) ExecutionMode() ToolExecutionMode { return t.mode }
func (t *fakeTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ ToolUpdateCallback) (AgentToolResult, error) {
	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	if t.execErr != nil {
		return AgentToolResult{}, t.execErr
	}
	return AgentToolResult{Content: t.content, IsError: t.isError}, nil
}

// ─── AfterToolCall hooks ─────────────────────────────────────────────────

// ─── Parallel tool execution ────────────────────────────────────────────

// TestParallelResultsInSourceOrder: results must be in source order even if tools
// complete out of order.
func TestParallelResultsInSourceOrder(t *testing.T) {
	// slow finishes later, fast finishes first: results must still be [slow, fast].
	names := []struct{ id, name string }{{"tc-s", "slow"}, {"tc-f", "fast"}}
	tools := []AgentTool{
		&fakeTool{name: "slow", mode: ToolModeParallel, delay: 80 * time.Millisecond, content: "slow-result"},
		&fakeTool{name: "fast", mode: ToolModeParallel, delay: 0, content: "fast-result"},
	}
	prov := providerFromSeqs(toolCallSeq(names...), textSeq("done"))
	a := NewAgent(AgentOptions{Model: fakeTestModel(prov), Tools: tools, MaxTurns: 5})

	msgs, err := a.Send(context.Background(), "order")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var results []string
	for _, m := range msgs {
		if m.ToolResult != nil {
			results = append(results, m.ToolResult.Text())
		}
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(results), results)
	}
	if results[0] != "slow-result" || results[1] != "fast-result" {
		t.Errorf("out of order: %v", results)
	}
}

// TestOnMessagePersistFiresPerProducedMessage proves the per-message persistence
// hook fires incrementally as each message is produced: the user prompt (via
// the runLoop message_end replay), assistant (toolUse), tool result, assistant
// (text): driven by message_end in emit(). This guards the incremental-
// persistence fix: batching persistence at turn end lost an entire in-flight
// turn when the process was killed mid-turn and resumed.
func TestOnMessagePersistFiresPerProducedMessage(t *testing.T) {
	tools := []AgentTool{&fakeTool{name: "echo", mode: ToolModeParallel, content: "echoed"}}
	prov := providerFromSeqs(toolCallSeq(struct{ id, name string }{"tc1", "echo"}), textSeq("done"))

	var persisted []string
	a := NewAgent(AgentOptions{
		Model:    fakeTestModel(prov),
		Tools:    tools,
		MaxTurns: 5,
		OnMessagePersist: func(m AgentMessage) error {
			switch {
			case m.User != nil:
				persisted = append(persisted, "user")
			case m.Assistant != nil:
				persisted = append(persisted, "assistant")
			case m.ToolResult != nil:
				persisted = append(persisted, "tool")
			}
			return nil
		},
	})

	if _, err := a.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Every message is persisted once via message_end, including the user
	// prompt. Production order:
	// user→assistant→tool→assistant.
	want := []string{"user", "assistant", "tool", "assistant"}
	if len(persisted) != len(want) {
		t.Fatalf("hook fired %d times %v, want %d %v", len(persisted), persisted, len(want), want)
	}
	for i := range want {
		if persisted[i] != want[i] {
			t.Fatalf("hook order[%d]=%q, want %q (full: %v)", i, persisted[i], want[i], persisted)
		}
	}
}

// TestOnMessagePersistSkipsResumedHistory proves the message_end-driven persist
// does NOT re-persist loaded/resumed context. On resume, history is loaded via
// SetMessages (already on disk); only THIS run's new messages (the prompt and
// the new assistant) must fire the hook. The runLoop replay covers
// a.messages[runStart:], never the loaded prefix, so resuming a long session
// does not duplicate every prior entry.
func TestOnMessagePersistSkipsResumedHistory(t *testing.T) {
	prov := providerFromSeqs(textSeq("reply"))
	var persisted []string
	a := NewAgent(AgentOptions{
		Model:    fakeTestModel(prov),
		MaxTurns: 5,
		OnMessagePersist: func(m AgentMessage) error {
			persisted = append(persisted, m.Role())
			return nil
		},
	})

	// Simulate resume: two already-persisted messages loaded into context.
	a.SetMessages([]AgentMessage{
		{User: &UserMessage{Role: RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: "old prompt"}}}},
		{Assistant: &AssistantMessage{Role: RoleAssistant, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "old reply"}}, StopReason: "stop"}},
	})

	if _, err := a.Send(context.Background(), "new prompt"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Only the new prompt + new assistant persist; the 2 loaded entries do not.
	want := []string{"user", "assistant"}
	if len(persisted) != len(want) {
		t.Fatalf("hook fired %d times %v, want %d %v (resumed history must not re-persist)",
			len(persisted), persisted, len(want), want)
	}
	for i := range want {
		if persisted[i] != want[i] {
			t.Fatalf("hook order[%d]=%q, want %q (full: %v)", i, persisted[i], want[i], persisted)
		}
	}
}

// ─── 4.x: stopReason plumbing ─────────────────────────────────────────────────

// TestStopReasonAborted verifies that context cancellation produces
// StopReason="aborted" on the partial assistant message.
// Strategy: pre-cancel the context, send a partial stream (EventStart only),
// close the channel. consumeStream's post-loop ctx.Err() check fires and sets
// StopReason="aborted" before returning.
func TestStopReasonAborted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so ctx.Err() is already set

	partial := agentTestAssistant(nil, ai.StopReasonPending)
	stream := ai.NewAssistantMessageEventStream()
	if err := stream.Push(ai.StartEvent{Partial: partial}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{}
	msg, _, err := a.consumeStream(ctx, stream, fakeTestModel(cancelledStartProvider{}))
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if msg.StopReason != "aborted" {
		t.Errorf("expected StopReason=aborted, got %q", msg.StopReason)
	}
}

// ─── Encrypted Reasoning Flow Test ───────────────────────────────────────────
// Verifies that thought signatures flow through the full agent loop:
// SSE stream → consumeStream → AssistantMessage.Content → ToolCall.ThoughtSignature

// abortingBatch runs a two-call batch whose first before hook cancels the
// run, and returns the tool results and the started call IDs.
func abortingBatch(t *testing.T, mode ToolExecutionMode) ([]ToolResultMessage, []string, int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beforeCalls atomic.Int32
	var starts []string
	rec := newEventRecorder(func(ev AgentEvent) {
		if start, ok := ev.(ToolExecutionStartEvent); ok {
			starts = append(starts, start.ToolCallID)
		}
	})
	a := NewAgent(AgentOptions{
		Model: scriptedModel(&scriptedProvider{respond: toolCallsThenText(toolCall("call-1", "a", nil), toolCall("call-2", "b", nil))}),
		Tools: []AgentTool{
			&scriptTool{name: "a", mode: mode, params: map[string]any{"type": "object"}},
			&scriptTool{name: "b", mode: mode, params: map[string]any{"type": "object"}},
		},
		EventCh: rec.ch,
		BeforeToolCall: []BeforeToolCallHook{
			func(_ context.Context, _, _ string, _ json.RawMessage) ToolCallHookResult {
				if beforeCalls.Add(1) == 1 {
					cancel()
				}
				return ToolCallHookResult{}
			},
		},
	})
	msgs, _ := a.Send(ctx, "run")
	rec.stop()
	var results []ToolResultMessage
	for _, m := range msgs {
		if m.ToolResult != nil {
			results = append(results, *m.ToolResult)
		}
	}
	return results, starts, beforeCalls.Load()
}

func checkAbortedBatch(t *testing.T, results []ToolResultMessage, starts []string, beforeCalls int32) {
	t.Helper()
	if len(results) != 1 || results[0].Text() != "Operation aborted" || !results[0].IsError {
		t.Fatalf("results = %+v, want one aborted error result", results)
	}
	if !reflect.DeepEqual(starts, []string{"call-1"}) || beforeCalls != 1 {
		t.Fatalf("started %v, before hook calls %d; want only call-1", starts, beforeCalls)
	}
}

// Parallel execution stops preparing calls once the run is
// aborted, so later calls never start.
func TestExecuteParallelSkipsUnstartedCallsAfterContextAbort(t *testing.T) {
	results, starts, beforeCalls := abortingBatch(t, ToolModeParallel)
	checkAbortedBatch(t, results, starts, beforeCalls)
}

func TestNormalizeMessagesSynthesizesMissingToolResults(t *testing.T) {
	msgs := []AgentMessage{
		{Assistant: &AssistantMessage{Role: RoleAssistant, StopReason: "toolUse", Content: []ai.AssistantContentBlock{
			ai.ToolCall{ID: "call_1", Name: "read", Arguments: ai.JsonObject{"path": "x.go"}},
		}}},
		{User: &UserMessage{Role: RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: "interrupt"}}}},
	}
	got := NormalizeMessages(msgs, nil)
	if len(got) != 3 {
		t.Fatalf("len(NormalizeMessages) = %d, want assistant + synthetic tool result + user: %#v", len(got), got)
	}
	if got[1].ToolResult == nil {
		t.Fatalf("middle message should be synthetic tool result: %#v", got[1])
	}
	res := got[1].ToolResult
	if res.ToolCallID != "call_1" || res.ToolName != "read" || res.Text() != "No result provided" || !res.IsError {
		t.Fatalf("synthetic tool result = %#v", res)
	}
}
