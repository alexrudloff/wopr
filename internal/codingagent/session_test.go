package codingagent

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func tempSessionMgr(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	return NewSessionManagerWithDir(dir, dir)
}

func mkUserMsg(text string) agent.AgentMessage {
	return agent.AgentMessage{
		User: &agent.UserMessage{
			Role:      "user",
			Content:   []ai.UserContentBlock{ai.TextContent{Text: text}},
			Timestamp: time.Now().UnixMilli(),
		},
	}
}

func mkAssistantMsg(text string) agent.AgentMessage {
	return agent.AgentMessage{
		Assistant: &agent.AssistantMessage{
			Role:      "assistant",
			Content:   []ai.AssistantContentBlock{ai.TextContent{Text: text}},
			Timestamp: time.Now().UnixMilli(),
		},
	}
}

// flushSession persists a freshly-created session by appending an
// assistant message, which triggers the first disk write. Tests asserting on-disk
// state must flush first, mirroring real usage where a session is only
// written once the model replies. Without it a fresh session has no file.
func flushSession(t *testing.T, sess *Session) {
	t.Helper()
	if _, err := sess.AppendMessage(mkAssistantMsg("ok")); err != nil {
		t.Fatalf("flush session: %v", err)
	}
}

// ─── Header shape ──────────────────────────────────────────────────────────────

// ─── Round-trip ────────────────────────────────────────────────────────────────

func TestSessionRoundTrip(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, err := sm.Create("sess-rt", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, err := sess.AppendMessage(mkAssistantMsg("msg" + string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	// Snapshot on-disk bytes.
	original, err := os.ReadFile(sess.Path())
	if err != nil {
		t.Fatal(err)
	}

	// Reload + verify entry count + leaf state.
	sm2 := NewSessionManagerWithDir(sm.cwd, sm.sessionDir)
	loaded, err := sm2.Load(sess.Path())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(loaded.Entries()); got != 5 {
		t.Errorf("entries: %d want 5", got)
	}
	if loaded.LeafID() == nil {
		t.Errorf("leafID nil after load")
	}

	// Lines should be byte-equal to what was written (no reformatting on load).
	again, _ := os.ReadFile(sess.Path())
	if !equalBytes(original, again) {
		t.Errorf("byte-equality lost on read-back")
	}
}

// ─── Message entry shape ──────────────────────────────────────────────────────

// ─── Fork ──────────────────────────────────────────────────────────────────────

// ─── Clone ─────────────────────────────────────────────────────────────────────

// forkToNewSessionFile branches the picked user message into a NEW file
// at its parent (excluding the message itself) and returns the message
// text for editor prefill, as /fork does (position "before").
func TestForkToNewSessionFile_BranchesAtParentIntoNewFile(t *testing.T) {
	sm := tempSessionMgr(t)
	src, _ := sm.Create("sess-fork-src", "")
	_, _ = src.AppendMessage(mkUserMsg("u1"))
	a1, _ := src.AppendMessage(mkAssistantMsg("a1"))
	u2, _ := src.AppendMessage(mkUserMsg("u2 SELECTED"))
	_, _ = src.AppendMessage(mkAssistantMsg("a2"))

	newSess, selectedText, err := sm.ForkToNewSession(src, u2)
	if err != nil {
		t.Fatalf("forkToNewSessionFile: %v", err)
	}
	if newSess.Path() == src.Path() {
		t.Errorf("/fork must create a NEW file, got same path as source")
	}
	if newSess.ParentSession() != src.Path() {
		t.Errorf("parentSession=%q want %q", newSess.ParentSession(), src.Path())
	}
	if selectedText != "u2 SELECTED" {
		t.Errorf("selectedText=%q want %q", selectedText, "u2 SELECTED")
	}
	// Branch is up to a1 (parent of u2): the selected message and its
	// reply are excluded so the user can re-submit an edited version.
	if leaf := newSess.LeafID(); leaf == nil || *leaf != a1 {
		t.Errorf("new leaf=%v want a1=%s", leaf, a1)
	}
	if _, ok := newSess.EntryByID(u2); ok {
		t.Errorf("forked session must exclude the selected message u2")
	}
}

// ─── BuildContext ─────────────────────────────────────────────────────────────

func TestBuildContextLeafOnFork(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, _ := sm.Create("sess-ctx-fork", "")
	id1, _ := sess.AppendMessage(mkUserMsg("u1"))
	_, _ = sess.AppendMessage(mkUserMsg("abandoned"))
	_ = sess.Fork(id1)
	_, _ = sess.AppendMessage(mkUserMsg("alt"))

	ctx := sess.BuildContext(nil)
	if len(ctx) != 2 {
		t.Errorf("context len=%d want 2 (u1 + alt; abandoned excluded)", len(ctx))
	}
	if extractUserText(ctx[1]) != "alt" {
		t.Errorf("expected 'alt' as leaf; got %q", extractUserText(ctx[1]))
	}
}

// ─── ListSessions / FindMostRecent / FindByID ─────────────────────────────────

// ─── Tree ──────────────────────────────────────────────────────────────────────

// ─── Helpers used in tests ────────────────────────────────────────────────────

func extractUserText(m agent.AgentMessage) string {
	if m.User == nil {
		return ""
	}
	for _, c := range m.User.Content {
		raw, _ := json.Marshal(c)
		var probe struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &probe)
		return probe.Text
	}
	return ""
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ─── AgentMessage wire round-trip ────────────────────────────────────────────

func TestAgentMessageRoundTripAssistantKeepsModelProviderAndUsage(t *testing.T) {
	in := agent.AgentMessage{
		Assistant: &agent.AssistantMessage{
			Role:      "assistant",
			Content:   []ai.AssistantContentBlock{ai.TextContent{Text: "hi"}},
			Timestamp: 1234,
			Provider:  "github-copilot",
			ModelID:   "claude-sonnet-4.5",
			Usage:     &ai.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 40, TotalTokens: 190},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out agent.AgentMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Assistant == nil {
		t.Fatal("expected assistant after round-trip")
	}
	if out.Assistant.Provider != "github-copilot" || out.Assistant.ModelID != "claude-sonnet-4.5" {
		t.Fatalf("round-trip model/provider = %q/%q", out.Assistant.Provider, out.Assistant.ModelID)
	}
	if out.Assistant.Usage == nil || out.Assistant.Usage.TotalTokens != 190 {
		t.Fatalf("round-trip usage = %+v", out.Assistant.Usage)
	}
}

func TestAgentMessageRoundTripToolResult(t *testing.T) {
	in := agent.AgentMessage{
		ToolResult: &agent.ToolResultMessage{
			Role:       agent.RoleToolResult,
			ToolCallID: "call-1",
			ToolName:   "search",
			Content: []ai.ToolResultMessageContent{
				ai.TextContent{Text: "ok output"},
				ai.ImageContent{MimeType: "image/png", Data: "QUJD"},
			},
			Timestamp: 1234,
		},
	}
	raw, _ := json.Marshal(in)
	var out agent.AgentMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ToolResult == nil {
		t.Fatalf("expected ToolResult after round-trip; got %+v", out)
	}
	if out.ToolResult.ToolCallID != "call-1" {
		t.Errorf("tool_call_id lost")
	}
	// ToolName and Images must survive persistence and resume: Gemini's
	// functionResponse.name needs the name, and tool-result images must replay.
	if out.ToolResult.ToolName != "search" {
		t.Errorf("tool_name lost on round-trip: %q", out.ToolResult.ToolName)
	}
	images := out.ToolResult.Images()
	if len(images) != 1 || images[0].Data != "QUJD" {
		t.Errorf("images lost on round-trip: %+v", images)
	}
}

func TestSessionResumePersistedMessagesReadbackable(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, _ := sm.Create("sess-resume", "")
	_, _ = sess.AppendMessage(mkUserMsg("hello"))
	_, _ = sess.AppendMessage(mkAssistantMsg("hi back"))
	_, _ = sess.AppendMessage(mkUserMsg("how are you"))

	// Resume from disk into a fresh manager.
	sm2 := NewSessionManagerWithDir(sm.cwd, sm.sessionDir)
	loaded, err := sm2.Load(sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	ctx := loaded.BuildContext(nil)
	if len(ctx) != 3 {
		t.Fatalf("resumed context len=%d want 3", len(ctx))
	}
	if extractUserText(ctx[0]) != "hello" {
		t.Errorf("ctx[0]=%q", extractUserText(ctx[0]))
	}
	if ctx[1].Assistant == nil {
		t.Errorf("ctx[1] not assistant; got %+v", ctx[1])
	}
	if extractUserText(ctx[2]) != "how are you" {
		t.Errorf("ctx[2]=%q", extractUserText(ctx[2]))
	}
}

// ─── bash_execution entry persistence ─────────────────────────────

// ─── 3.2e: AppendCompaction, AppendBranchSummary, BuildContext ordering ───────

func TestAppendCompaction(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, err := sm.Create("sess-compaction-test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add two messages first so firstKeptEntryID can reference the second.
	idA, err := sess.AppendMessage(mkUserMsg("hello"))
	if err != nil {
		t.Fatalf("AppendMessage A: %v", err)
	}
	idB, err := sess.AppendMessage(mkAssistantMsg("world"))
	if err != nil {
		t.Fatalf("AppendMessage B: %v", err)
	}

	compID, err := sess.AppendCompaction("compact summary", idB, 1500, nil, false, &ai.Usage{Input: 111, Output: 22, CacheRead: 3})
	if err != nil {
		t.Fatalf("AppendCompaction: %v", err)
	}

	// Leaf must have advanced to the compaction entry.
	if leaf := sess.LeafID(); leaf == nil || *leaf != compID {
		t.Errorf("leaf after compaction: got %v want %q", leaf, compID)
	}

	// Verify the stored entry round-trips correctly.
	e, ok := sess.EntryByID(compID)
	if !ok {
		t.Fatalf("entry %q not found", compID)
	}
	var comp CompactionEntry
	if err := json.Unmarshal(e.Raw(), &comp); err != nil {
		t.Fatalf("unmarshal CompactionEntry: %v", err)
	}
	if comp.Summary != "compact summary" {
		t.Errorf("Summary: got %q want %q", comp.Summary, "compact summary")
	}
	if comp.FirstKeptEntryID != idB {
		t.Errorf("FirstKeptEntryID: got %q want %q", comp.FirstKeptEntryID, idB)
	}
	if comp.TokensBefore != 1500 {
		t.Errorf("TokensBefore: got %d want 1500", comp.TokensBefore)
	}
	// Summarization usage must persist so compaction cost counts toward the
	// session total on reload.
	if comp.Usage == nil || comp.Usage.Input != 111 || comp.Usage.Output != 22 || comp.Usage.CacheRead != 3 {
		t.Errorf("Usage: got %+v want input 111 output 22 cacheRead 3", comp.Usage)
	}
	if comp.Type != "compaction" {
		t.Errorf("Type: got %q want %q", comp.Type, "compaction")
	}

	// Parent must be idA's successor (idB).
	_ = idA // used above for ordering; compaction parent should be idB
	if comp.ParentID == nil || *comp.ParentID != idB {
		t.Errorf("ParentID: got %v want %q", comp.ParentID, idB)
	}

	// Reload and verify persistence.
	loaded, err := loadSessionFile(sess.path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	var found bool
	for _, ent := range loaded.entries {
		if ent.Base.ID == compID {
			found = true
			var c2 CompactionEntry
			if err := json.Unmarshal(ent.Raw(), &c2); err != nil {
				t.Fatalf("reload unmarshal: %v", err)
			}
			if c2.FirstKeptEntryID != idB {
				t.Errorf("reloaded FirstKeptEntryID: got %q want %q", c2.FirstKeptEntryID, idB)
			}
		}
	}
	if !found {
		t.Error("compaction entry not found in reloaded session")
	}
}

func TestAppendBranchSummary(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, err := sm.Create("sess-bs-test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	idA, err := sess.AppendMessage(mkUserMsg("first"))
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Pass idA explicitly as the parent (nil is not auto-wired to the
	// current leaf).
	bsID, err := sess.AppendBranchSummary(&idA, "branch summary text", nil, false, nil)
	if err != nil {
		t.Fatalf("AppendBranchSummary: %v", err)
	}

	// Leaf must be the new branch_summary entry.
	if leaf := sess.LeafID(); leaf == nil || *leaf != bsID {
		t.Errorf("leaf after branch_summary: got %v want %q", leaf, bsID)
	}

	e, ok := sess.EntryByID(bsID)
	if !ok {
		t.Fatalf("entry %q not found", bsID)
	}
	var bs BranchSummaryEntry
	if err := json.Unmarshal(e.Raw(), &bs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bs.Summary != "branch summary text" {
		t.Errorf("Summary: got %q want %q", bs.Summary, "branch summary text")
	}
	if bs.Type != "branch_summary" {
		t.Errorf("Type: got %q want %q", bs.Type, "branch_summary")
	}

	// Explicit parentID = idA must be preserved.
	if bs.ParentID == nil || *bs.ParentID != idA {
		t.Errorf("ParentID: got %v want %q", bs.ParentID, idA)
	}
	if bs.FromID != idA {
		t.Errorf("FromID: got %q want %q", bs.FromID, idA)
	}
}

// TestBuildContextCompactionOrder verifies the ordering rule:
// summary first, then kept entries before compaction (from firstKeptEntryID),
// then entries after compaction. The compaction node itself is NOT emitted.
//
// Chain: A(user) → B(user) → C(compaction, firstKeptEntryID=B) → D(user) → E(user)
// Expected messages: [compaction_summary, B, D, E]
func TestBuildContextCompactionOrder(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, err := sm.Create("sess-ctx-order", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	idA, err := sess.AppendMessage(mkUserMsg("A"))
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	idB, err := sess.AppendMessage(mkUserMsg("B"))
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	_, err = sess.AppendCompaction("the summary", idB, 999, nil, false, nil)
	if err != nil {
		t.Fatalf("compaction: %v", err)
	}
	idD, err := sess.AppendMessage(mkUserMsg("D"))
	if err != nil {
		t.Fatalf("D: %v", err)
	}
	idE, err := sess.AppendMessage(mkUserMsg("E"))
	if err != nil {
		t.Fatalf("E: %v", err)
	}
	_ = idA // A is before firstKeptEntryID=B and should NOT appear
	_ = idD
	_ = idE

	msgs := sess.BuildContext(nil)

	if len(msgs) != 4 {
		t.Fatalf("BuildContext: got %d messages want 4: %+v", len(msgs), msgs)
	}

	// Message 0: compaction summary.
	if msgs[0].Custom == nil || msgs[0].Custom["role"] != agent.RoleCompactionSummary {
		t.Fatalf("msg[0]: expected compactionSummary, got %+v", msgs[0])
	}
	if msgs[0].Custom["summary"] != "the summary" {
		t.Errorf("msg[0]: summary = %v", msgs[0].Custom["summary"])
	}

	// Message 1: B (firstKeptEntryID).
	if msgs[1].User == nil {
		t.Fatal("msg[1]: expected user message for B")
	}
	txt1, ok := msgs[1].User.Content[0].(ai.TextContent)
	if !ok {
		t.Fatalf("msg[1]: expected TextContent, got %T", msgs[1].User.Content[0])
	}
	if txt1.Text != "B" {
		t.Errorf("msg[1]: got %q want %q", txt1.Text, "B")
	}

	// Message 2: D (after compaction).
	txt2, ok := msgs[2].User.Content[0].(ai.TextContent)
	if !ok {
		t.Fatalf("msg[2]: expected TextContent, got %T", msgs[2].User.Content[0])
	}
	if txt2.Text != "D" {
		t.Errorf("msg[2]: got %q want %q", txt2.Text, "D")
	}

	// Message 3: E.
	txt3, ok := msgs[3].User.Content[0].(ai.TextContent)
	if !ok {
		t.Fatalf("msg[3]: expected TextContent, got %T", msgs[3].User.Content[0])
	}
	if txt3.Text != "E" {
		t.Errorf("msg[3]: got %q want %q", txt3.Text, "E")
	}
}
