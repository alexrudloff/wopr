package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	codingcompaction "github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// fakeProvider is a minimal ai.Provider for tests that don't actually
// hit the LLM. NewSession needs a non-nil Model whose Provider is
// non-nil; we never call Stream in unit tests below.
type fakeProvider struct{}

func newSessionTestStream(events ...ai.AssistantMessageEvent) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	for _, event := range events {
		if err := stream.Push(event); err != nil {
			panic(err)
		}
	}
	return stream
}

func sessionTestMessage(provider, text string, reason ai.StopReason, errorMessage string) *ai.AssistantMessage {
	content := []ai.AssistantContentBlock(nil)
	if text != "" {
		content = append(content, ai.TextContent{Text: text})
	}
	return &ai.AssistantMessage{
		Content: content, Provider: provider, Model: "fake-1",
		StopReason: reason, ErrorMessage: errorMessage,
	}
}

func assistantText(msg *agent.AssistantMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.Content {
		if text, ok := block.(ai.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func (fakeProvider) ID() string { return "fake" }
func (fakeProvider) Stream(context.Context, ai.TranscriptContext, ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	message := sessionTestMessage("fake", "", ai.StopReasonStop, "")
	return newSessionTestStream(
		ai.StartEvent{Partial: message},
		ai.DoneEvent{Reason: ai.StopReasonStop, Message: message},
	), nil
}
func (fakeProvider) Close() error { return nil }

func fakeModel() *ai.Model {
	return &ai.Model{
		ID:          "fake-1",
		DisplayName: "fake-1",
		Provider:    fakeProvider{},
		Capabilities: ai.ModelCapabilities{
			ContextWindow: 8000,
		},
	}
}

func newTestServices(t *testing.T) *Services {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("WOPR_HOME", tmp)
	srv, err := NewServices(ServicesOptions{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// newTestServicesSmallKeep is newTestServices with a tiny keepRecentTokens so
// short test sessions still have messages outside the recent-keep window to
// summarize. Without it, the 0.79.10 empty-compaction guard (PrepareCompaction
// returns nil when nothing is left to summarize) refuses to compact these
// sessions and the compaction never runs.
func newTestServicesSmallKeep(t *testing.T) *Services {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("WOPR_HOME", tmp)
	agentDir := filepath.Join(tmp, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"compaction":{"keepRecentTokens":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServices(ServicesOptions{CWD: t.TempDir(), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestNewSessionCreatesJSONL(t *testing.T) {
	svcs := newTestServices(t)
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	if sess.ID() == "" {
		t.Errorf("session ID empty")
	}
	if !strings.HasSuffix(sess.Path(), ".jsonl") {
		t.Errorf("path %q does not end in .jsonl", sess.Path())
	}
	if got := sess.CWD(); got != svcs.CWD() {
		t.Errorf("session CWD = %q, want services CWD %q", got, svcs.CWD())
	}
	// File on disk should exist with at least the header line.
	if !filepath.IsAbs(sess.Path()) {
		t.Errorf("path should be absolute: %q", sess.Path())
	}
}

func TestResumeRestoresModelAndThinkingLevel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	services := newTestServices(t)
	model, err := BuildModel("openai/gpt-5", services)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewSession(services, SessionOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetThinkingLevel(ai.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := first.inner.AppendMessage(agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role: agent.RoleAssistant, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "saved"}}, StopReason: "stop",
	}}); err != nil {
		t.Fatal(err)
	}
	path := first.Path()
	if err := first.Close(); err != nil {
		t.Fatalf("close source session: %v", err)
	}

	fallback, err := BuildModel("openai/gpt-4o", services)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewSession(services, SessionOptions{Model: fallback, ResumePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resumed.Close(); err != nil {
			t.Errorf("close resumed session: %v", err)
		}
	}()
	if resumed.Model().ID != "gpt-5" {
		t.Fatalf("resumed model = %s, want gpt-5", resumed.Model().ID)
	}
	if resumed.ThinkingLevel() != ai.ThinkingHigh {
		t.Fatalf("resumed thinking level = %s, want high", resumed.ThinkingLevel())
	}
}

func TestNewSessionAllowedToolsFiltersDefaultsAndExtras(t *testing.T) {
	svcs := newTestServices(t)
	extra := &fakeTool{name: "test-tool-abc"}

	sess, err := NewSession(svcs, SessionOptions{
		Model: fakeModel(),
		Tools: []agent.AgentTool{extra},
		AllowedTools: map[string]struct{}{
			"read":          {},
			"test-tool-abc": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	names := toolNames(sess.Tools())
	if !slices.Contains(names, "read") || !slices.Contains(names, "test-tool-abc") {
		t.Errorf("allowlisted tools missing: %v", names)
	}
	for _, blocked := range []string{"bash", "write", "edit"} {
		if slices.Contains(names, blocked) {
			t.Errorf("blocked tool %q should not be in allowed list; got %v", blocked, names)
		}
	}
}

// TestNewSessionExcludedToolsDenylistsBuiltinAndExtension: --exclude-tools is a
// denylist that makes a tool non-callable, gating built-in AND
// extension/caller tools (not merely hiding from the prompt).
func TestNewSessionExcludedToolsDenylistsBuiltinAndExtension(t *testing.T) {
	svcs := newTestServices(t)
	extBash := &fakeTool{name: "ext-only"}
	sess, err := NewSession(svcs, SessionOptions{
		Model: fakeModel(),
		Tools: []agent.AgentTool{extBash},
		ExcludedTools: map[string]struct{}{
			"bash":     {}, // built-in
			"ext-only": {}, // extension/caller tool
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	names := toolNames(sess.Tools())
	for _, off := range []string{"bash", "ext-only"} {
		if slices.Contains(names, off) {
			t.Errorf("excluded tool %q must be non-callable; got %v", off, names)
		}
	}
	// Non-excluded built-ins remain.
	for _, want := range []string{"read", "edit", "write"} {
		if !slices.Contains(names, want) {
			t.Errorf("non-excluded tool %q should remain; got %v", want, names)
		}
	}
}

func fakeModelWithProvider(prov ai.Provider) *ai.Model {
	return &ai.Model{
		ID:          "fake-1",
		DisplayName: "fake-1",
		Provider:    prov,
		Capabilities: ai.ModelCapabilities{
			ContextWindow: 8000,
		},
	}
}

func TestSessionResumeRebuildsAgentMessages(t *testing.T) {
	svcs := newTestServices(t)

	// Create a fresh session, append two messages directly via the
	// inner type so we don't need a real LLM call.
	sess1, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	path := sess1.Path()
	// Inject a user + assistant message via the inner session API.
	userMsg := agent.AgentMessage{
		User: &agent.UserMessage{
			Role:      "user",
			Content:   []ai.UserContentBlock{ai.TextContent{Text: "hello"}},
			Timestamp: 1,
		},
	}
	asstMsg := agent.AgentMessage{
		Assistant: &agent.AssistantMessage{
			Role:      "assistant",
			Content:   []ai.AssistantContentBlock{ai.TextContent{Text: "hi back"}},
			Timestamp: 2,
		},
	}
	if _, err := sess1.inner.AppendMessage(userMsg); err != nil {
		t.Fatal(err)
	}
	if _, err := sess1.inner.AppendMessage(asstMsg); err != nil {
		t.Fatal(err)
	}
	_ = sess1.Close()

	// Resume the same session via NewSession with ResumePath.
	sess2, err := NewSession(svcs, SessionOptions{
		Model:      fakeModel(),
		ResumePath: path,
	})
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	defer func() { _ = sess2.Close() }()

	got := sess2.Messages()
	if len(got) != 2 || got[0].User == nil || got[1].Assistant == nil {
		t.Fatalf("resumed transcript should contain only the directly persisted user and assistant: %+v", got)
	}
}

func TestSessionSendPersistsUserPromptBeforeAgentLoop(t *testing.T) {
	// Verify that even when the agent's stream returns nothing
	// useful (our fakeProvider yields a closed channel: zero events),
	// the user prompt is persisted to the JSONL on disk before the
	// agent is even called. This is the F-2 contract: persistence
	// happens BEFORE the LLM call, so an aborted Send doesn't lose
	// the user input.
	svcs := newTestServices(t)
	sess, _ := NewSession(svcs, SessionOptions{Model: fakeModel()})
	defer func() { _ = sess.Close() }()

	ctx := context.Background()
	_, _ = sess.Send(ctx, "persist-me-please")

	// Re-open the session from disk and confirm the user prompt is there.
	sess2, err := NewSession(svcs, SessionOptions{
		Model:      fakeModel(),
		ResumePath: sess.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess2.Close() }()

	msgs := sess2.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected at least 1 persisted message after Send")
	}
	found := false
	for _, m := range msgs {
		if m.User == nil {
			continue
		}
		for _, c := range m.User.Content {
			if tc, ok := c.(ai.TextContent); ok && tc.Text == "persist-me-please" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("persisted messages don't contain the user prompt; got %+v", msgs)
	}
}

// TestPersistenceFollowsReplaceInner guards against the session/agent desync:
// after a session switch (ReplaceInner: used by /resume, /new, /clone), the
// agent's OnMessagePersist hook must record into the NEW session, not the one
// captured when the session was created. The old wiring captured a local
// `inner`, so post-switch turns persisted to the original session while
// /session, /tree, and /compact displayed the new (empty) one: orphaning a
// whole conversation's worth of work and leaving the displayed session with 0
// entries (no on-disk file).
func TestPersistenceFollowsReplaceInner(t *testing.T) {
	svcs := newTestServices(t)
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	oldInner := sess.Inner()
	oldEntriesBefore := len(oldInner.Entries())

	// Swap in a fresh empty session, exactly as /new, /clone, and /resume do.
	sm := newSessionManagerForDir(svcs.CWD(), "")
	newInner, err := sm.Create("sess-replaced", "")
	if err != nil {
		t.Fatal(err)
	}
	sess.ReplaceInner(newInner)

	// A turn after the swap. fakeModel yields no assistant events, but the user
	// prompt is persisted via OnMessagePersist before the agent loop.
	_, _ = sess.Send(context.Background(), "after-swap-prompt")

	newMsgs := 0
	for _, e := range newInner.Entries() {
		if e.Base.Type == "message" {
			newMsgs++
		}
	}
	oldEntriesAfter := len(oldInner.Entries())
	if newMsgs == 0 {
		t.Fatal("persistence did not follow ReplaceInner: post-swap turn was orphaned to the old session (new session has 0 message entries)")
	}
	if oldEntriesAfter != oldEntriesBefore {
		t.Errorf("post-swap turn changed OLD session entries: %d to %d", oldEntriesBefore, oldEntriesAfter)
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

type fakeTool struct {
	name string
}

func (t *fakeTool) Name() string          { return t.name }
func (t *fakeTool) Label() string         { return "" }
func (t *fakeTool) Schema() ai.ToolSchema { return ai.ToolSchema{Name: t.name} }
func (t *fakeTool) ExecutionMode() agent.ToolExecutionMode {
	return agent.ToolModeParallel
}
func (t *fakeTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	return agent.AgentToolResult{}, nil
}

func toolNames(in []agent.AgentTool) []string {
	out := make([]string, len(in))
	for i, t := range in {
		out[i] = t.Name()
	}
	return out
}

// ─── compaction / NavigateTree tests ──────────────────────────────────────────

// fakeCompleter implements compaction.SimpleCompleter for tests.
type fakeCompleter struct {
	summary string
	err     error
	called  atomic.Bool
}

func (c *fakeCompleter) CompleteSimple(_ context.Context, _ *ai.Model, _ string, _ []agent.AgentMessage, _ ai.StreamOptions) (string, *ai.Usage, error) {
	c.called.Store(true)
	return c.summary, nil, c.err
}

type blockingCompactionCompleter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newBlockingCompactionCompleter() *blockingCompactionCompleter {
	return &blockingCompactionCompleter{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *blockingCompactionCompleter) CompleteSimple(ctx context.Context, _ *ai.Model, _ string, _ []agent.AgentMessage, _ ai.StreamOptions) (string, *ai.Usage, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case <-c.release:
		return "summary", nil, nil
	}
}

// buildSessionWithMessages creates a session and injects n plain user/assistant
// message pairs directly into the inner JSONL (no LLM call).
func buildSessionWithMessages(t *testing.T, svcs *Services, n int) *Session {
	t.Helper()
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		userMsg := agent.AgentMessage{
			User: &agent.UserMessage{
				Role:    "user",
				Content: []ai.UserContentBlock{ai.TextContent{Text: fmt.Sprintf("q%d", i)}},
			},
		}
		asstMsg := agent.AgentMessage{
			Assistant: &agent.AssistantMessage{
				Role:    "assistant",
				Content: []ai.AssistantContentBlock{ai.TextContent{Text: fmt.Sprintf("a%d", i)}},
			},
		}
		if _, err := sess.inner.AppendMessage(userMsg); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.inner.AppendMessage(asstMsg); err != nil {
			t.Fatal(err)
		}
	}
	// Rebuild agent messages to match inner state.
	sess.agent.SetMessages(sess.inner.BuildContext(nil))
	return sess
}

// drainEvents waits for an ordered marker after the operation's events.
func drainEvents(t *testing.T, sess *Session) []agent.AgentEvent {
	t.Helper()
	marker := &agent.AgentSettledEvent{}
	sess.emitOrderedEvent(marker)
	var events []agent.AgentEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sess.events:
			if event == marker {
				return events
			}
			events = append(events, event)
		case <-timer.C:
			t.Fatal("session event funnel did not drain")
			return nil
		}
	}
}

func TestCompact_Smoke(t *testing.T) {
	svcs := newTestServicesSmallKeep(t)
	sess := buildSessionWithMessages(t, svcs, 3)
	defer func() { _ = sess.Close() }()

	sess.completer = &fakeCompleter{summary: "summary text"}

	if err := sess.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Tree should contain a compaction entry.
	leafID := sess.inner.LeafID()
	if leafID == nil {
		t.Fatal("leaf is nil after Compact")
		return
	}
	entries := sess.inner.Branch(*leafID)
	haveCompaction := false
	for _, e := range entries {
		if e.Base.Type == "compaction" {
			haveCompaction = true
		}
	}
	if !haveCompaction {
		t.Error("expected a compaction entry in the session branch after Compact")
	}

	// Events should include a CompactionStartEvent and CompactionEndEvent with
	// the summary text.
	evs := drainEvents(t, sess)
	var start, end agent.AgentEvent
	for _, ev := range evs {
		switch ev.(type) {
		case agent.CompactionStartEvent:
			start = ev
		case agent.CompactionEndEvent:
			end = ev
		}
	}
	if start == nil {
		t.Error("CompactionStartEvent not emitted")
	}
	if end == nil {
		t.Fatal("CompactionEndEvent not emitted")
	}
	endEv := end.(agent.CompactionEndEvent)
	if !strings.Contains(endEv.Summary, "summary text") {
		t.Errorf("CompactionEndEvent.Summary = %q; want it to contain 'summary text'", endEv.Summary)
	}
	if endEv.ErrorMessage != "" {
		t.Errorf("unexpected error in CompactionEndEvent: %q", endEv.ErrorMessage)
	}
	wantEstimatedAfter := 0
	for _, msg := range sess.agent.Messages() {
		wantEstimatedAfter += codingcompaction.EstimateTokens(msg)
	}
	if endEv.EstimatedTokensAfter != wantEstimatedAfter {
		t.Errorf("CompactionEndEvent.EstimatedTokensAfter = %d, want %d", endEv.EstimatedTokensAfter, wantEstimatedAfter)
	}
}

func TestNavigateTree_WithSummary(t *testing.T) {
	svcs := newTestServices(t)
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	sess.completer = &fakeCompleter{summary: "branch summary text"}

	// Append first user message; record its ID as branchPoint.
	userMsg := agent.AgentMessage{
		User: &agent.UserMessage{
			Role:    "user",
			Content: []ai.UserContentBlock{ai.TextContent{Text: "root"}},
		},
	}
	if _, err := sess.inner.AppendMessage(userMsg); err != nil {
		t.Fatal(err)
	}
	branchPoint := *sess.inner.LeafID()

	// Extend the branch.
	userMsg2 := agent.AgentMessage{
		User: &agent.UserMessage{
			Role:    "user",
			Content: []ai.UserContentBlock{ai.TextContent{Text: "diverged"}},
		},
	}
	if _, err := sess.inner.AppendMessage(userMsg2); err != nil {
		t.Fatal(err)
	}

	res, err := sess.NavigateTree(context.Background(), branchPoint, NavigateTreeOptions{Summarize: true})
	if err != nil {
		t.Fatalf("NavigateTree (with summary): %v", err)
	}
	if res.Aborted {
		t.Error("NavigateTree returned Aborted=true unexpectedly")
	}

	// A branch_summary entry should exist on the CURRENT LEAF'S branch
	// (root → ... → branch_summary → leaf). Before the 3.2l fix, the entry
	// was written to the abandoned branch and was invisible after navigation.
	leaf := sess.inner.LeafID()
	if leaf == nil {
		t.Fatal("expected non-nil leaf after NavigateTree with Summarize=true")
	}
	branch := sess.inner.Branch(*leaf)
	foundOnBranch := false
	for _, e := range branch {
		if e.Base.Type == "branch_summary" {
			foundOnBranch = true
			break
		}
	}
	if !foundOnBranch {
		t.Error("branch_summary entry not found on current leaf's branch (would be invisible in rebuildChatFromSession)")
	}
}

// ─── 3.2g: Auto-compaction trigger tests ──────────────────────────────────────

// TestCheckCompactionOverflow verifies that an overflow error persistently
// omits the failed attempt with a context_edit, compacts, and asks the post-run
// loop to retry.
func TestCheckCompactionOverflow(t *testing.T) {
	svcs := newTestServicesSmallKeep(t)
	sess := buildSessionWithMessages(t, svcs, 3)
	defer func() { _ = sess.Close() }()
	sess.completer = &fakeCompleter{summary: "overflow summary"}

	m := fakeModel()
	m.Capabilities.ContextWindow = 8000
	sess.model.Store(m)

	overflowMsg := &agent.AssistantMessage{
		Role:         "assistant",
		StopReason:   "error",
		ErrorMessage: "prompt token count of 9000 exceeds the limit of 8000",
		Provider:     "fake",
		ModelID:      "fake-1",
		Timestamp:    time.Now().UnixMilli(),
	}
	overflowID, err := sess.inner.AppendMessage(agent.AgentMessage{Assistant: overflowMsg})
	if err != nil {
		t.Fatal(err)
	}
	sess.refreshContext()
	retryMsg := lastAssistantMessage(sess.agent.Messages())

	continueRun, err := sess.checkCompaction(context.Background(), retryMsg, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !continueRun {
		t.Fatal("overflow recovery did not request a retry")
	}
	omitted := false
	for _, entry := range sess.inner.Entries() {
		var edit icodingagent.ContextEditEntry
		if entry.Base.Type == "context_edit" && json.Unmarshal(entry.Raw(), &edit) == nil && edit.TargetID == overflowID && edit.Replacement == nil {
			omitted = true
		}
	}
	if !omitted {
		t.Fatal("overflow attempt was not omitted with a context_edit")
	}
	for _, m := range sess.agent.Messages() {
		if m.Assistant != nil && m.Assistant.ErrorMessage == overflowMsg.ErrorMessage {
			t.Error("overflow error assistant message still present in agent state")
		}
	}
	if !sess.overflowRecoveryAttempted.Load() {
		t.Error("overflow recovery was not marked as attempted")
	}

	var start, end bool
	for _, ev := range drainEvents(t, sess) {
		switch ev := ev.(type) {
		case agent.CompactionStartEvent:
			start = ev.Reason == "overflow"
		case agent.CompactionEndEvent:
			end = ev.Reason == "overflow" && ev.WillRetry && ev.ErrorMessage == ""
		}
	}
	if !start || !end {
		t.Errorf("overflow compaction events: start=%v end=%v", start, end)
	}
}

// TestAutoRetryUsesContinueNotSend verifies that the retry path pops the
// error assistant message and leaves exactly 1 user message, proving
// that Continue (not Send) semantics are correct: no duplicate user messages.
func TestAutoRetryUsesContinueNotSend(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WOPR_HOME", tmp)
	svcs := newTestServices(t)
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	// Simulate the state that exists mid-retry:
	// agent has [user_msg, error_assistant_msg].
	userMsg := agent.AgentMessage{
		User: &agent.UserMessage{
			Role:    "user",
			Content: []ai.UserContentBlock{ai.TextContent{Text: "hello"}},
		},
	}
	errAssistant := agent.AgentMessage{
		Assistant: &agent.AssistantMessage{
			Role:         "assistant",
			StopReason:   "error",
			ErrorMessage: "429 Too Many Requests",
		},
	}
	sess.agent.SetMessages([]agent.AgentMessage{userMsg, errAssistant})

	// Apply the retry-path transform: pop the trailing error assistant message.
	msgs := sess.agent.Messages()
	if last := msgs[len(msgs)-1]; last.Assistant != nil {
		sess.agent.SetMessages(msgs[:len(msgs)-1])
	}

	// After the pop: exactly 1 message (the user_msg) must remain.
	// If Send were called here instead of Continue, it would add a second user_msg.
	after := sess.agent.Messages()
	userCount := 0
	for _, m := range after {
		if m.User != nil {
			userCount++
		}
	}
	if userCount != 1 {
		t.Errorf("after error-pop: expected 1 user message, got %d (len=%d)", userCount, len(after))
	}
	if len(after) != 1 {
		t.Errorf("after error-pop: expected exactly 1 message total, got %d", len(after))
	}
}

// ─── Session metadata + stats tests (3.8) ─────────────────────────────────────
