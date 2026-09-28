package coding

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
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// Session recovery and context accounting tests.

// scriptedResponse builds one faux provider response from the request.
type scriptedResponse func(request []ai.Message) *ai.AssistantMessage

// scriptedProvider answers each model call with the next scripted response,
// It records every request.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []scriptedResponse
	requests  []string
}

func (p *scriptedProvider) ID() string   { return "faux" }
func (p *scriptedProvider) Close() error { return nil }

func (p *scriptedProvider) Stream(_ context.Context, request ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.mu.Lock()
	messages := request.Messages()
	raw, _ := json.Marshal(messages)
	p.requests = append(p.requests, string(raw))
	index := len(p.requests) - 1
	p.mu.Unlock()
	message := &ai.AssistantMessage{Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonStop, ErrorMessage: "no scripted response", Timestamp: time.Now().UnixMilli()}
	if index < len(p.responses) {
		message = p.responses[index](messages)
	}
	if message.StopReason == ai.StopReasonError {
		return newSessionTestStream(ai.StartEvent{Partial: message}, ai.ErrorEvent{Reason: ai.StopReasonError, Error: message}), nil
	}
	return newSessionTestStream(ai.StartEvent{Partial: message}, ai.DoneEvent{Reason: message.StopReason, Message: message}), nil
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func fauxReply(text string, reason ai.StopReason, offset time.Duration) scriptedResponse {
	return func([]ai.Message) *ai.AssistantMessage {
		var content []ai.AssistantContentBlock
		if text != "" {
			content = append(content, ai.TextContent{Text: text})
		}
		return &ai.AssistantMessage{Content: content, Provider: "faux", Model: "faux-1", StopReason: reason, Timestamp: time.Now().Add(offset).UnixMilli()}
	}
}

func fauxError(message string) scriptedResponse {
	return func([]ai.Message) *ai.AssistantMessage {
		return &ai.AssistantMessage{Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonError, ErrorMessage: message, Timestamp: time.Now().UnixMilli()}
	}
}

type harnessOptions struct {
	settings      string
	contextWindow int
	maxTokens     int
	tools         []agent.AgentTool
}

type recoveryHarness struct {
	session  *Session
	provider *scriptedProvider
	mu       sync.Mutex
	events   []agent.AgentEvent
	done     chan struct{}
}

func newRecoveryHarness(t *testing.T, opts harnessOptions, responses ...scriptedResponse) *recoveryHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WOPR_HOME", home)
	agentDir := filepath.Join(home, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := opts.settings
	if settings == "" {
		settings = "{}"
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	services, err := NewServices(ServicesOptions{CWD: t.TempDir(), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: responses}
	contextWindow := opts.contextWindow
	if contextWindow == 0 {
		contextWindow = 128_000
	}
	model := &ai.Model{ID: "faux-1", DisplayName: "faux-1", Provider: provider, Capabilities: ai.ModelCapabilities{ContextWindow: contextWindow, MaxOutputTokens: opts.maxTokens}}
	session, err := NewSession(services, SessionOptions{Model: model, SkipBuiltinTools: true, Tools: opts.tools})
	if err != nil {
		t.Fatal(err)
	}
	h := &recoveryHarness{session: session, provider: provider, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for event := range session.Events() {
			h.mu.Lock()
			h.events = append(h.events, event)
			h.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = session.Close()
		<-h.done
	})
	return h
}

// settle waits until the Session's ordered agent_settled event is published.
func (h *recoveryHarness) settle(t *testing.T) []agent.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, event := range h.events {
			if _, ok := event.(agent.AgentSettledEvent); ok {
				events := append([]agent.AgentEvent(nil), h.events...)
				h.mu.Unlock()
				return events
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("agent_settled was not published")
	return nil
}

func (h *recoveryHarness) entries(entryType string) []icodingagent.SessionEntry {
	var out []icodingagent.SessionEntry
	for _, entry := range h.session.Inner().Entries() {
		if entry.Base.Type == entryType {
			out = append(out, entry)
		}
	}
	return out
}

func messageEntryIDs(t *testing.T, h *recoveryHarness, match func(*agent.AssistantMessage) bool) []string {
	t.Helper()
	var ids []string
	for _, entry := range h.entries("message") {
		message, ok := entry.AsMessage()
		if ok && message.Message.Assistant != nil && match(message.Message.Assistant) {
			ids = append(ids, entry.Base.ID)
		}
	}
	return ids
}

func projectionContains(h *recoveryHarness, text string) bool {
	raw, _ := json.Marshal(h.session.Inner().BuildSessionProjection().Messages)
	return strings.Contains(string(raw), text)
}

func TestBoundaryDoesNotTriggerThresholdCompactionFromPostEditUsageCapturedBeforeALaterCompaction(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"compaction":{"enabled":true,"keepRecentTokens":1,"reserveTokens":0}}`, contextWindow: 10_000, maxTokens: 100})
	inner := h.session.Inner()
	userID, err := inner.AppendMessage(agent.AgentMessage{User: &agent.UserMessage{Role: agent.RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: "small input"}}, Timestamp: time.Now().UnixMilli() - 3}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inner.AppendContextEdit(userID, &icodingagent.ContextEditReplacement{Content: json.RawMessage(`"edited input"`)}); err != nil {
		t.Fatal(err)
	}
	response := &agent.AssistantMessage{Role: agent.RoleAssistant, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "answer"}}, Provider: "faux", ModelID: "faux-1",
		Usage: &ai.Usage{Input: 50_000, Output: 1, TotalTokens: 50_001}, StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli() - 2}
	if _, err := inner.AppendMessage(agent.AgentMessage{Assistant: response}); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.AppendCompaction("small summary", userID, 50_001, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	h.session.RefreshContext()
	errorMessage := &agent.AssistantMessage{Role: agent.RoleAssistant, Provider: "faux", ModelID: "faux-1", StopReason: ai.StopReasonError, ErrorMessage: "invalid_api_key", Timestamp: time.Now().UnixMilli() + 1_000}

	if continueRun, err := h.session.checkCompaction(context.Background(), errorMessage, true, nil); err != nil || continueRun {
		t.Fatalf("checkCompaction = %v, %v", continueRun, err)
	}
	if n := len(h.entries("compaction")); n != 1 {
		t.Fatalf("compactions = %d, want the original 1", n)
	}
}

func TestDurableRecoveryMarksTheExhaustedRetryRunAsFinal(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":true,"maxRetries":1,"baseDelayMs":1}}`},
		fauxError("overloaded_error"),
		fauxError("overloaded_error"),
	)

	_, _ = h.session.Send(context.Background(), "start")
	events := h.settle(t)

	var willRetry []bool
	retryFailed := false
	for _, event := range events {
		switch event := event.(type) {
		case agent.AgentEndEvent:
			willRetry = append(willRetry, event.WillRetry)
		case agent.AutoRetryEndEvent:
			retryFailed = retryFailed || (!event.Success && event.Attempt == 1)
		}
	}
	if len(willRetry) != 2 || !willRetry[0] || willRetry[1] {
		t.Fatalf("agent_end willRetry = %v, want [true false]", willRetry)
	}
	if !retryFailed {
		t.Fatal("auto_retry_end{success:false, attempt:1} missing")
	}
}

func TestDurableRecoveryKeepsOmissionsAndDoesNotRetryWhenRecoveryCompactionFails(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{
		settings:      `{"compaction":{"keepRecentTokens":1,"reserveTokens":0},"retry":{"enabled":false,"maxRetries":0,"baseDelayMs":1}}`,
		contextWindow: 1000, maxTokens: 100,
	},
		fauxReply("partial response", ai.StopReasonLength, 0),
		fauxError("summary failed"),
		fauxReply("must not retry", ai.StopReasonStop, 0),
	)

	if _, err := h.session.Send(context.Background(), strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	h.settle(t)

	if len(h.entries("context_edit")) == 0 {
		t.Fatal("no context_edit omission was persisted")
	}
	if len(h.entries("compaction")) != 0 {
		t.Fatal("a failed recovery compaction appended an entry")
	}
	if ids := messageEntryIDs(t, h, func(m *agent.AssistantMessage) bool { return assistantText(m) == "partial response" }); len(ids) != 1 {
		t.Fatalf("raw partial response entries = %d, want 1", len(ids))
	}
	if projectionContains(h, "partial response") {
		t.Fatal("projection still contains the omitted partial response")
	}
	if got := h.provider.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}

}

func TestQueuedUserMessageResetsRecoveryBudget(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{})
	h.session.overflowRecoveryAttempted.Store(true)
	if err := h.session.persistMessage(agent.AgentMessage{User: &agent.UserMessage{
		Role: agent.RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: "queued follow-up"}},
	}}); err != nil {
		t.Fatalf("persist queued user message: %v", err)
	}
	if h.session.overflowRecoveryAttempted.Load() {
		t.Fatal("new user message retained the previous recovery budget")
	}
}
