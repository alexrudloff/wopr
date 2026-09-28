package codingagent

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// ─── classifyKey (extended: base cases in input_split_test.go) ───────────────

// ─── resolveOutcome ───────────────────────────────────────────────────────────

// StdinBuffer base cases live in input_split_test.go.
// Extended cases for bracketed paste:

// ─── filterAllowedTools ───────────────────────────────────────────────────────

// stubTool implements agent.AgentTool for testing.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                           { return s.name }
func (s *stubTool) Label() string                          { return "" }
func (s *stubTool) Schema() ai.ToolSchema                  { return ai.ToolSchema{Name: s.name} }
func (s *stubTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }
func (s *stubTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	return agent.AgentToolResult{}, nil
}

// Verify stubTool satisfies AgentTool at compile time.
var _ agent.AgentTool = (*stubTool)(nil)

// ─── levelsForModel / maxThinkingIndex ────────────────────────────────────────

// ─── thinkingLevelToAI ───────────────────────────────────────────────────────

// ─── shortenPath ──────────────────────────────────────────────────────────────

// ─── jsonNoEscape ─────────────────────────────────────────────────────────────

type capturedStreamRequest struct {
	ai.StreamOptions
	Messages []ai.Message
}

type captureStreamOptionsProvider struct {
	seen chan capturedStreamRequest
}

func (p captureStreamOptionsProvider) ID() string { return "capture" }

func (p captureStreamOptionsProvider) Stream(_ context.Context, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.seen <- capturedStreamRequest{StreamOptions: options, Messages: transcript.Messages()}
	partial := &ai.AssistantMessage{Provider: p.ID(), Model: "capture", StopReason: ai.StopReasonPending}
	final := &ai.AssistantMessage{Provider: p.ID(), Model: "capture", StopReason: ai.StopReasonStop}
	stream := ai.NewAssistantMessageEventStream()
	if err := stream.Push(ai.StartEvent{Partial: partial}); err != nil {
		return nil, err
	}
	if err := stream.Push(ai.DoneEvent{Reason: ai.StopReasonStop, Message: final}); err != nil {
		return nil, err
	}
	return stream, nil
}

func (p captureStreamOptionsProvider) Close() error { return nil }

func lastUserMessageText(t *testing.T, messages []ai.Message) string {
	t.Helper()
	if len(messages) == 0 {
		t.Fatal("provider received no messages")
	}
	last, ok := messages[len(messages)-1].(ai.UserMessage)
	if !ok {
		t.Fatalf("last provider message = %T, want ai.UserMessage", messages[len(messages)-1])
	}
	switch content := last.Content.(type) {
	case ai.UserText:
		return string(content)
	case ai.UserContentBlocks:
		for _, block := range content {
			if text, ok := block.(ai.TextContent); ok {
				return text.Text
			}
		}
	}
	t.Fatal("last provider user message had no text block")
	return ""
}

func TestInteractiveMode_EnterDuringCompactionUsesCompactionQueue(t *testing.T) {
	m := NewInteractiveMode(InteractiveOptions{CWD: t.TempDir()})
	m.chatContainer = tui.NewContainer()
	m.statusContainer = tui.NewContainer()
	m.pendingMessagesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.statusLine = NewStatusLine(nil, nil)
	m.editor = tui.NewEditor()
	m.agent = agent.NewAgent(agent.AgentOptions{})
	m.keybindings = DefaultKeybindingsManager()
	m.isCompacting = true
	m.isIdle = true
	m.editor.SetText("send after compaction")

	if err := m.dispatchKey(context.Background(), "\r"); err != nil {
		t.Fatal(err)
	}

	if got := m.compactionQueue; len(got) != 1 || got[0].text != "send after compaction" || got[0].mode != compactionQueueSteer {
		t.Fatalf("Enter during compaction queued %v, want one compaction steering message", got)
	}
	steering, followUps := m.agent.PendingMessages()
	if len(steering) != 0 || len(followUps) != 0 {
		t.Fatalf("Enter during compaction leaked into agent queues: steering=%v followUps=%v", steering, followUps)
	}
}

func TestInteractiveMode_QueuedEnterSendsAfterCompactionEnd(t *testing.T) {
	seen := make(chan capturedStreamRequest, 1)
	model := &ai.Model{
		ID:           "capture-compaction-enter",
		DisplayName:  "capture-compaction-enter",
		Provider:     captureStreamOptionsProvider{seen: seen},
		Capabilities: ai.ModelCapabilities{ContextWindow: 8000},
	}
	m := NewInteractiveMode(InteractiveOptions{CWD: t.TempDir(), Model: model})
	m.chatContainer = tui.NewContainer()
	m.statusContainer = tui.NewContainer()
	m.pendingMessagesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.statusLine = NewStatusLine(model, nil)
	m.editor = tui.NewEditor()
	m.agent = agent.NewAgent(agent.AgentOptions{Model: model})
	m.keybindings = DefaultKeybindingsManager()
	m.runCtx = context.Background()
	m.abortCtx, m.abortFn = context.WithCancel(m.runCtx)
	m.isCompacting = true
	m.isIdle = true
	m.editor.SetText("send after compaction")

	if err := m.dispatchKey(context.Background(), "\r"); err != nil {
		t.Fatal(err)
	}
	m.handleAgentEvent(agent.CompactionEndEvent{Aborted: true, Reason: "manual"})

	select {
	case options := <-seen:
		if got := lastUserMessageText(t, options.Messages); got != "send after compaction" {
			t.Fatalf("post-compaction turn started with %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message entered during compaction did not start automatically after compaction ended")
	}
}

func TestAC49CompactionEndWillRetryQueuesMessageForRetryTurn(t *testing.T) {
	m := NewInteractiveMode(InteractiveOptions{CWD: t.TempDir()})
	m.chatContainer = tui.NewContainer()
	m.statusContainer = tui.NewContainer()
	m.pendingMessagesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.statusLine = NewStatusLine(nil, nil)
	m.agent = agent.NewAgent(agent.AgentOptions{})
	m.keybindings = DefaultKeybindingsManager()
	m.runCtx = context.Background()
	m.isCompacting = true
	m.compactionQueue = []compactionQueuedMessage{
		{text: "steer after retry compaction", mode: compactionQueueSteer},
		{text: "follow up after retry compaction", mode: compactionQueueFollowUp},
	}

	m.handleAgentEvent(agent.CompactionEndEvent{Aborted: true, Reason: "auto", WillRetry: true})

	steering, followUps := m.agent.PendingMessages()
	if len(steering) != 1 || len(followUps) != 1 {
		t.Fatalf("retry queue = steering:%v followUps:%v", steering, followUps)
	}
	if got := extractAgentMessageText(steering[0]); got != "steer after retry compaction" {
		t.Fatalf("retry queued message = %q", got)
	}
	if got := extractAgentMessageText(followUps[0]); got != "follow up after retry compaction" {
		t.Fatalf("retry follow-up message = %q", got)
	}
	if len(m.compactionQueue) != 0 {
		t.Fatalf("compaction queue was not drained: %v", m.compactionQueue)
	}
}

// A message queued during compaction must still be sent when compaction ends
// after a turn has just finished.
//
// isIdle is reset through a queued runOnMain, so it reads stale-false for a
// window after the run goroutine exits. Auto-compaction that runs at the end of
// a turn lands in exactly that window. flushCompactionQueue used isIdle to
// decide whether a turn was running, so it took the steer path, and steering
// only drains inside a running turn: with the goroutine already gone the
// message reached neither the UI nor the session, and a restart lost it.
//
// turnActive is the signal that means what this decision needs, marking exactly
// the interval in which a run goroutine exists to drain the queue.
func TestCompactionFlushStartsTurnWhenIsIdleIsStaleFalse(t *testing.T) {
	dir := t.TempDir()
	seen := make(chan capturedStreamRequest, 1)
	model := &ai.Model{
		ID:           "capture-stale",
		DisplayName:  "capture-stale",
		Provider:     captureStreamOptionsProvider{seen: seen},
		Capabilities: ai.ModelCapabilities{ContextWindow: 8000},
	}
	m := NewInteractiveMode(InteractiveOptions{CWD: dir, Model: model})
	m.chatContainer = tui.NewContainer()
	m.statusContainer = tui.NewContainer()
	m.pendingMessagesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.statusLine = NewStatusLine(model, nil)
	m.agent = agent.NewAgent(agent.AgentOptions{Model: model})
	m.runCtx = context.Background()
	m.abortCtx = context.Background()
	m.abortFn = func() {}

	// The state auto-compaction leaves at the end of a turn: the run goroutine
	// has exited, so nothing will drain a steering queue, but the queued
	// runOnMain that restores isIdle has not run yet.
	m.isIdle = false
	m.turnActive.Store(false)

	m.compactionQueue = []compactionQueuedMessage{{text: "sent after compaction", mode: compactionQueueSteer}}
	m.flushCompactionQueue(m.runCtx, true)

	select {
	case opts := <-seen:
		if got := lastUserMessageText(t, opts.Messages); got != "sent after compaction" {
			t.Fatalf("turn started with wrong prompt: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no turn started: the queued message was stranded in a queue with no goroutine to drain it")
	}
	if len(m.compactionQueue) != 0 {
		t.Fatalf("queue should be cleared after flush, got %v", m.compactionQueue)
	}
}
