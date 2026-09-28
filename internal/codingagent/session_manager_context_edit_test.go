package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Session context-edit cases that exercise the session manager alone. The
// estimate and compaction-preparation
// cases live in internal/codingagent/compaction.

func contextEditAssistant(text string) agent.AgentMessage {
	return agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role:       agent.RoleAssistant,
		Content:    []ai.AssistantContentBlock{ai.TextContent{Text: text}},
		API:        "faux",
		Provider:   "faux",
		ModelID:    "faux",
		Usage:      &ai.Usage{Input: 10, Output: 1, TotalTokens: 11},
		StopReason: ai.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}}
}

func contextEditUser(text string) agent.AgentMessage {
	return agent.AgentMessage{User: &agent.UserMessage{
		Role:      agent.RoleUser,
		Content:   []ai.UserContentBlock{ai.TextContent{Text: text}},
		Timestamp: time.Now().UnixMilli(),
	}}
}

func replacementText(t *testing.T, text string) *ContextEditReplacement {
	t.Helper()
	raw, err := marshalJSONLine(text)
	if err != nil {
		t.Fatal(err)
	}
	return &ContextEditReplacement{Content: raw}
}

func replacementBlocks(t *testing.T, text string) *ContextEditReplacement {
	t.Helper()
	raw, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		t.Fatal(err)
	}
	return &ContextEditReplacement{Content: raw}
}

func mustAppendContextMessage(t *testing.T, sess *Session, message agent.AgentMessage) string {
	t.Helper()
	id, err := sess.AppendMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustEdit(t *testing.T, sess *Session, targetID string, replacement *ContextEditReplacement) string {
	t.Helper()
	id, err := sess.AppendContextEdit(targetID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustCompact(t *testing.T, sess *Session, summary, firstKept string, tokensBefore int) string {
	t.Helper()
	id, err := sess.AppendCompaction(summary, firstKept, tokensBefore, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// projectedText reads a projected message's text or summary.
func projectedText(message agent.AgentMessage) string {
	var out strings.Builder
	switch {
	case message.User != nil:
		for _, block := range message.User.Content {
			if text, ok := block.(ai.TextContent); ok {
				out.WriteString(text.Text)
			}
		}
	case message.Assistant != nil:
		for _, block := range message.Assistant.Content {
			if text, ok := block.(ai.TextContent); ok {
				out.WriteString(text.Text)
			}
		}
	case message.ToolResult != nil:
		for _, block := range message.ToolResult.Content {
			if text, ok := block.(ai.TextContent); ok {
				out.WriteString(text.Text)
			}
		}
	case message.Custom != nil:
		if summary, ok := message.Custom["summary"].(string); ok {
			return summary
		}
		if content, ok := message.Custom["content"].(string); ok {
			return content
		}
	}
	return out.String()
}

func projectedRoles(messages []agent.AgentMessage) []string {
	roles := make([]string, len(messages))
	for i, message := range messages {
		roles[i] = message.Role()
	}
	return roles
}

func projectedTexts(messages []agent.AgentMessage) []string {
	texts := make([]string, len(messages))
	for i, message := range messages {
		texts[i] = projectedText(message)
	}
	return texts
}

func TestSessionContextEditOmitsATargetOnlyFromModelProjection(t *testing.T) {
	sess := NewSession("s", t.TempDir())
	mustAppendContextMessage(t, sess, contextEditUser("request"))
	assistantID := mustAppendContextMessage(t, sess, contextEditAssistant("partial"))
	result := agent.AgentMessage{ToolResult: &agent.ToolResultMessage{
		Role:       agent.RoleToolResult,
		ToolCallID: "call-1",
		ToolName:   "read",
		Content:    []ai.ToolResultMessageContent{ai.TextContent{Text: "raw output"}},
		Details:    map[string]any{"path": "large.txt"},
		IsError:    true,
		Timestamp:  time.Now().UnixMilli(),
	}}
	resultID := mustAppendContextMessage(t, sess, result)
	resultRaw, _ := sess.EntryByID(resultID)
	rawBefore := string(resultRaw.Raw())
	mustEdit(t, sess, assistantID, nil)
	mustEdit(t, sess, resultID, nil)

	messages := 0
	for _, entry := range sess.Branch(*sess.LeafID()) {
		if entry.Base.Type == "message" {
			messages++
		}
	}
	if messages != 3 {
		t.Fatalf("branch message entries = %d, want 3", messages)
	}
	if got := projectedRoles(sess.BuildSessionProjection().Messages); !slices.Equal(got, []string{"user"}) {
		t.Fatalf("projected roles = %v, want [user]", got)
	}
	after, _ := sess.EntryByID(resultID)
	if string(after.Raw()) != rawBefore {
		t.Fatalf("target entry changed:\n%s\n%s", rawBefore, after.Raw())
	}
}

func TestSessionContextEditReplacesOnlyContentAndLetsTheLatestEditWin(t *testing.T) {
	sess := NewSession("s", t.TempDir())
	targetID := mustAppendContextMessage(t, sess, contextEditAssistant("original"))
	mustEdit(t, sess, targetID, replacementBlocks(t, "first"))
	mustEdit(t, sess, targetID, nil)
	mustEdit(t, sess, targetID, replacementBlocks(t, "restored"))

	projected := sess.BuildSessionProjection().Messages
	if len(projected) != 1 || projected[0].Assistant == nil {
		t.Fatalf("projection = %#v, want one assistant", projected)
	}
	if got := projectedText(projected[0]); got != "restored" {
		t.Fatalf("projected text = %q, want restored", got)
	}
	if projected[0].Assistant.Usage == nil || projected[0].Assistant.Usage.TotalTokens != 11 {
		t.Fatalf("replacement changed usage: %#v", projected[0].Assistant.Usage)
	}
	raw, _ := sess.EntryByID(targetID)
	original, _ := raw.AsMessage()
	if got := projectedText(original.Message); got != "original" {
		t.Fatalf("raw entry text = %q, want original", got)
	}
}

func TestSessionContextEditKeepsEditsBranchRelative(t *testing.T) {
	sess := NewSession("s", t.TempDir())
	targetID := mustAppendContextMessage(t, sess, contextEditUser("original"))
	mustEdit(t, sess, targetID, replacementText(t, "edited"))
	if got := projectedText(sess.BuildSessionProjection().Messages[0]); got != "edited" {
		t.Fatalf("edited projection = %q", got)
	}
	if err := sess.Fork(targetID); err != nil {
		t.Fatal(err)
	}
	if got := projectedText(sess.BuildSessionProjection().Messages[0]); got != "original" {
		t.Fatalf("branch projection = %q, want original", got)
	}
}

func TestSessionContextEditAppliesPostCompactionEditsToRetainedPreCompactionEntries(t *testing.T) {
	sess := NewSession("s", t.TempDir())
	mustAppendContextMessage(t, sess, contextEditUser("summarized"))
	retainedID := mustAppendContextMessage(t, sess, contextEditUser("original retained"))
	mustCompact(t, sess, "summary", retainedID, 100)
	mustEdit(t, sess, retainedID, replacementText(t, "edited retained"))

	if got := projectedTexts(sess.BuildSessionProjection().Messages); !slices.Equal(got, []string{"summary", "edited retained"}) {
		t.Fatalf("texts = %v", got)
	}
}

func TestSessionContextEditRejectsInvalidTargetsAndReplacements(t *testing.T) {
	sess := NewSession("s", t.TempDir())
	userID := mustAppendContextMessage(t, sess, contextEditUser("first"))
	if err := sess.AppendModelSwitch("faux", "faux", ""); err != nil {
		t.Fatal(err)
	}
	modelChangeID := *sess.LeafID()
	otherID := mustAppendContextMessage(t, sess, contextEditUser("other branch"))
	if err := sess.Fork(userID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		target      string
		replacement *ContextEditReplacement
		want        string
	}{
		{"object content", userID, &ContextEditReplacement{Content: json.RawMessage(`{"text":"x"}`)}, "Context edit replacement must be null or contain string/array content"},
		{"missing content", userID, &ContextEditReplacement{}, "Context edit replacement must be null or contain string/array content"},
		{"unknown target", "missing", nil, "Entry missing not found"},
		{"off branch", otherID, nil, "Entry " + otherID + " is not on the active branch"},
	}
	for _, tc := range cases {
		if _, err := sess.AppendContextEdit(tc.target, tc.replacement); err == nil || err.Error() != tc.want {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := sess.Fork(modelChangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendContextEdit(modelChangeID, nil); err == nil || err.Error() != "Entry "+modelChangeID+" does not contribute editable model content" {
		t.Fatalf("model_change target: err = %v", err)
	}
}

func TestSessionContextEditRoundTripsSessionFilesByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"legacy-file","timestamp":"2024-12-03T14:00:00.000Z","cwd":"/work"}`,
		`{"type":"message","id":"c3d4e5f6","parentId":null,"timestamp":"2024-12-03T14:10:00.000Z","message":{"role":"user","content":"a <b> & c","timestamp":1733235000000}}`,
		`{"type":"context_edit","id":"f6g7h8i9","parentId":"c3d4e5f6","timestamp":"2024-12-03T14:10:30.000Z","targetId":"c3d4e5f6","replacement":{"content":[{"type":"text","text":"x < y"}]}}`,
		`{"type":"context_edit","id":"g6h7i8j9","parentId":"f6g7h8i9","timestamp":"2024-12-03T14:11:00.000Z","targetId":"c3d4e5f6","replacement":null}`,
		`{"type":"compaction","id":"h1","parentId":"g6h7i8j9","timestamp":"2024-12-03T14:12:00.000Z","summary":"s","firstKeptEntryId":"h1","tokensBefore":5,"fromHook":false,"systemMessage":{"role":"system","content":"sys","timestamp":1733235120000}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManagerWithDir(dir, dir)
	sess, err := sm.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range sess.Entries() {
		if string(entry.Raw()) != lines[i+1] {
			t.Fatalf("entry %d changed:\n got %s\nwant %s", i, entry.Raw(), lines[i+1])
		}
	}
	clone, err := sm.Clone(sess, "h1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(clone.Path())
	if err != nil {
		t.Fatal(err)
	}
	cloned := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if !slices.Equal(cloned[1:], lines[1:]) {
		t.Fatalf("clone rewrote entries:\n%s", strings.Join(cloned[1:], "\n"))
	}
	projected := clone.BuildSessionProjection().Messages
	if got := projectedRoles(projected); !slices.Equal(got, []string{"system", agent.RoleCompactionSummary}) {
		t.Fatalf("roles after retain-none compaction with system state = %v", got)
	}
}
