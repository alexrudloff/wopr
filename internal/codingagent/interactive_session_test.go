package codingagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// These tests run the interactive owner loop against a real coding.Session,
// the production pairing, with a scripted faux provider.

type scriptedReply func() *ai.AssistantMessage

type scriptedProvider struct {
	mu       sync.Mutex
	replies  []scriptedReply
	requests []string
}

func (p *scriptedProvider) ID() string   { return "faux" }
func (p *scriptedProvider) Close() error { return nil }

func (p *scriptedProvider) Stream(_ context.Context, request ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	raw, _ := json.Marshal(request.Messages())
	p.mu.Lock()
	p.requests = append(p.requests, string(raw))
	index := len(p.requests) - 1
	p.mu.Unlock()
	message := &ai.AssistantMessage{Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonError, ErrorMessage: "no scripted reply", Timestamp: time.Now().UnixMilli()}
	if index < len(p.replies) {
		message = p.replies[index]()
	}
	stream := ai.NewAssistantMessageEventStream()
	_ = stream.Push(ai.StartEvent{Partial: message})
	if message.StopReason == ai.StopReasonError {
		_ = stream.Push(ai.ErrorEvent{Reason: ai.StopReasonError, Error: message})
	} else {
		_ = stream.Push(ai.DoneEvent{Reason: message.StopReason, Message: message})
	}
	return stream, nil
}

func (p *scriptedProvider) requestLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func reply(text string, usage ai.Usage) scriptedReply {
	return func() *ai.AssistantMessage {
		return &ai.AssistantMessage{
			Content:  []ai.AssistantContentBlock{ai.TextContent{Text: text}},
			Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonStop, Usage: usage,
			Timestamp: time.Now().UnixMilli(),
		}
	}
}

func replyError(message string) scriptedReply {
	return func() *ai.AssistantMessage {
		return &ai.AssistantMessage{Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonError, ErrorMessage: message, Timestamp: time.Now().UnixMilli()}
	}
}

type sessionPair struct {
	session  *coding.Session
	provider *scriptedProvider
	harness  *icodingagent.TestHarness
}

func newSessionPair(t *testing.T, settings string, contextWindow int, onEvent func(*icodingagent.TestHarness, agent.AgentEvent), replies ...scriptedReply) *sessionPair {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WOPR_HOME", home)
	agentDir := filepath.Join(home, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{CWD: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{replies: replies}
	model := &ai.Model{ID: "faux-1", DisplayName: "faux-1", Provider: provider, Capabilities: ai.ModelCapabilities{ContextWindow: contextWindow}}
	session, err := coding.NewSession(services, coding.SessionOptions{Model: model, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	harness := icodingagent.NewTestHarness(t, icodingagent.InteractiveOptions{
		CWD: cwd, AgentDir: agentDir, Model: model,
		SessionHandle: session, SettingsManager: services.SettingsManager(),
	}, onEvent)
	t.Cleanup(func() { _ = session.Close() })
	return &sessionPair{session: session, provider: provider, harness: harness}
}

// A message typed while the end-of-turn auto-compaction runs is delivered to
// the model in the same run: the run continues while messages are queued
// after post-run handling and before settling. The interactive mode used to end the run with the message stranded in the
// steering queue and the session idle.
func TestInteractiveMessageQueuedDuringEndOfTurnCompactionIsDelivered(t *testing.T) {
	typed := false
	// The first answer's usage passes the threshold (window minus the default
	// 16384 reserve), so the run ends with a threshold compaction.
	pair := newSessionPair(t, `{"compaction":{"keepRecentTokens":1},"retry":{"enabled":false}}`, 100000,
		func(h *icodingagent.TestHarness, ev agent.AgentEvent) {
			if _, ok := ev.(agent.CompactionStartEvent); ok && !typed {
				typed = true
				h.Enter("typed during compaction")
			}
		},
		reply("first answer", ai.Usage{Input: 90000, Output: 10}),
		reply("compaction summary", ai.Usage{}),
		reply("second answer", ai.Usage{Input: 10, Output: 10}),
	)
	pair.harness.Do(func() { pair.harness.Enter(strings.Repeat("x", 5000)) })

	deadline := time.Now().Add(10 * time.Second)
	for len(pair.provider.requestLog()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pair.harness.WaitIdle(t, 10*time.Second)

	requests := pair.provider.requestLog()
	if len(requests) != 3 {
		steering, followUp := pair.harness.QueuedMessages()
		t.Fatalf("model requests = %d, want 3 (the queued message was stranded: steering=%d followUp=%d)", len(requests), steering, followUp)
	}
	if !strings.Contains(requests[2], "typed during compaction") {
		t.Fatalf("third request does not carry the queued message:\n%s", requests[2])
	}
	if steering, followUp := pair.harness.QueuedMessages(); steering != 0 || followUp != 0 {
		t.Fatalf("queues after settle: steering=%d followUp=%d, want empty", steering, followUp)
	}
}

// A retry that ends in a context overflow still gets overflow recovery, and
// the failed attempts are omitted durably. Post-run handling re-runs after
// every continuation (retry, then compaction check) and omits a retried
// attempt with a context_edit; the interactive mode's own
// retry loop reported the overflow as a successful retry and stopped.
func TestInteractiveRetryThenOverflowCompactsAndRetries(t *testing.T) {
	pair := newSessionPair(t, `{"compaction":{"keepRecentTokens":1},"retry":{"enabled":true,"maxRetries":3,"baseDelayMs":1,"maxDelayMs":1}}`, 100000, nil,
		replyError("overloaded_error"),
		replyError("prompt is too long: 213462 tokens > 200000 maximum"),
		reply("compaction summary", ai.Usage{}),
		reply("recovered answer", ai.Usage{Input: 10, Output: 10}),
	)
	pair.harness.Do(func() { pair.harness.Enter(strings.Repeat("x", 5000)) })

	deadline := time.Now().Add(10 * time.Second)
	for len(pair.provider.requestLog()) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pair.harness.WaitIdle(t, 10*time.Second)

	if got := len(pair.provider.requestLog()); got != 4 {
		t.Fatalf("model requests = %d, want 4 (retry, overflow compaction, recovered retry)", got)
	}
	entries := pair.session.Inner().Entries()
	var compactions, omissions int
	for _, entry := range entries {
		switch entry.Base.Type {
		case "compaction":
			compactions++
		case "context_edit":
			omissions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction entries = %d, want 1", compactions)
	}
	if omissions < 2 {
		t.Fatalf("context_edit entries = %d, want the retried and the overflowed attempts omitted", omissions)
	}
	if last := pair.session.LastAssistantText(); last == nil || *last != "recovered answer" {
		t.Fatalf("last assistant text = %v, want the recovered answer", last)
	}
}

// Esc while a run streams puts queued messages back in the editor before it
// aborts, so they are neither lost nor injected into the next, unrelated prompt.
func TestInteractiveEscRestoresQueuedMessagesToTheEditor(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	pair := newSessionPair(t, `{"retry":{"enabled":false}}`, 100000, nil,
		func() *ai.AssistantMessage {
			// The first request is in flight, so the run is streaming.
			close(requested)
			<-release
			return reply("first answer", ai.Usage{Input: 10, Output: 10})()
		},
		reply("next answer", ai.Usage{Input: 10, Output: 10}),
	)
	pair.harness.Do(func() { pair.harness.Enter("start") })
	<-requested
	pair.harness.Do(func() {
		pair.harness.Enter("also update the docs")
		// A running turn needs two Escape presses: the first arms the interrupt.
		pair.harness.Key("\x1b")
		pair.harness.Key("\x1b")
	})
	close(release)
	pair.harness.WaitIdle(t, 10*time.Second)

	if got := pair.harness.EditorText(); got != "also update the docs" {
		t.Fatalf("editor after Esc = %q, want the queued message restored", got)
	}
	if steering, followUp := pair.harness.QueuedMessages(); steering != 0 || followUp != 0 {
		t.Fatalf("queues after Esc: steering=%d followUp=%d, want empty", steering, followUp)
	}
	pair.harness.Do(func() { pair.harness.Enter("revert that") })
	pair.harness.WaitIdle(t, 10*time.Second)
	for _, request := range pair.provider.requestLog() {
		if strings.Contains(request, "also update the docs") {
			t.Fatalf("the restored message leaked into a model request:\n%s", request)
		}
	}
}
