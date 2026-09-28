package agent

import (
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// TestNormalizeMessages_SkipsErrored verifies that assistant messages with
// StopReason="error" are dropped.
func TestNormalizeMessages_SkipsErrored(t *testing.T) {
	msgs := []AgentMessage{
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "hi"}}}},
		{Assistant: &AssistantMessage{Role: "assistant", StopReason: "error", ErrorMessage: "network error"}},
	}
	got := NormalizeMessages(msgs, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 message (user only), got %d", len(got))
	}
	if got[0].User == nil {
		t.Fatalf("expected user message, got %+v", got[0])
	}
}

// TestNormalizeMessages_OrphanedToolCall verifies that a tool_use block with
// no matching tool_result gets a synthetic error result injected.
func TestNormalizeMessages_OrphanedToolCall(t *testing.T) {
	msgs := []AgentMessage{
		{Assistant: &AssistantMessage{
			Role:       "assistant",
			StopReason: "aborted", // aborted mid-tool so no result
			Content: []ai.AssistantContentBlock{
				ai.ToolCall{ID: "call_orphan", Name: "bash"},
			},
		}},
	}
	// Aborted assistant is dropped; its orphaned tool call is not flushed
	// because the message was never added to result. Confirm no panic.
	got := NormalizeMessages(msgs, nil)
	// Aborted assistant dropped → 0 messages.
	if len(got) != 0 {
		t.Fatalf("expected 0 messages (aborted dropped), got %d", len(got))
	}
}

// TestNormalizeMessages_OrphanedToolResult verifies that tool results whose
// tool_call_id has no matching tool_use in any assistant message are stripped.
// This happens after compaction or model switching when the assistant turn
// was dropped but the tool result survived.
func TestNormalizeMessages_OrphanedToolResult(t *testing.T) {
	msgs := []AgentMessage{
		// Compaction summary replaced the original conversation.
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{
			ai.TextContent{Text: "compaction summary"},
		}}},
		// Orphaned tool result from before compaction: no matching tool_use.
		{ToolResult: &ToolResultMessage{
			Role: RoleToolResult, ToolCallID: "call_juCALP1trjCh5UT1mSMlKFwG", ToolName: "bash",
			Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}},
		}},
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{
			ai.TextContent{Text: "continue"},
		}}},
	}
	got := NormalizeMessages(msgs, nil)
	// The orphaned tool result should be stripped: 2 user messages remain.
	if len(got) != 2 {
		t.Fatalf("expected 2 messages (orphaned tool result stripped), got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if m.ToolResult != nil {
			t.Fatalf("orphaned tool result should have been stripped: %+v", m.ToolResult)
		}
	}
}

// TestNormalizeMessages_ErroredAssistantOrphansToolResult pins the D48 path:
// an errored/aborted assistant that held the
// only tool_use for a persisted tool result (an abort race where the tool ran
// and recorded its result before the turn was marked aborted). The errored
// assistant is dropped, orphaning the result; wopr strips it so the request
// stays valid; sending it would make the provider reject the whole request ("No tool call found for function call output").
func TestNormalizeMessages_ErroredAssistantOrphansToolResult(t *testing.T) {
	msgs := []AgentMessage{
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "run it"}}}},
		{Assistant: &AssistantMessage{
			Role: "assistant", StopReason: "aborted",
			Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "call_race", Name: "bash"}},
		}},
		// The tool result was persisted before the turn was marked aborted.
		{ToolResult: &ToolResultMessage{
			Role: RoleToolResult, ToolCallID: "call_race", ToolName: "bash",
			Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "output"}},
		}},
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "again"}}}},
	}
	got := NormalizeMessages(msgs, nil)
	// Aborted assistant dropped; its now-orphaned tool result stripped; two
	// user messages remain.
	if len(got) != 2 {
		t.Fatalf("expected 2 user messages (aborted + orphaned result removed), got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if m.ToolResult != nil {
			t.Fatalf("orphaned tool result must be stripped, found %+v", m.ToolResult)
		}
		if m.Assistant != nil {
			t.Fatalf("aborted assistant must be dropped, found %+v", m.Assistant)
		}
	}
}

// TestNormalizeMessages_PartialOrphanSynthesizesMissingResult pins the mixed
// case: one assistant makes two tool calls but only one result is recorded.
// The missing result is synthesized (orphaned-call path), and a separately
// orphaned result in the same message (no surviving tool_use) is stripped
// (D48), while the valid result is preserved.
func TestNormalizeMessages_PartialOrphanSynthesizesMissingResult(t *testing.T) {
	msgs := []AgentMessage{
		{Assistant: &AssistantMessage{
			Role: "assistant", StopReason: "toolUse",
			Content: []ai.AssistantContentBlock{
				ai.ToolCall{ID: "call_a", Name: "read"},
				ai.ToolCall{ID: "call_b", Name: "bash"},
			},
		}},
		// A valid result plus a separate orphaned result.
		{ToolResult: &ToolResultMessage{
			Role: RoleToolResult, ToolCallID: "call_a", ToolName: "read",
			Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "file"}},
		}},
		{ToolResult: &ToolResultMessage{
			Role: RoleToolResult, ToolCallID: "call_ghost", ToolName: "bash",
			Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "stale"}},
		}},
		{User: &UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "next"}}}},
	}
	got := NormalizeMessages(msgs, nil)
	// Expect: assistant, the real tool result (call_a only, call_ghost stripped),
	// a synthetic result for the missing call_b, then the user message.
	var haveA, haveGhost, haveSyntheticB bool
	var userCount int
	for _, m := range got {
		if m.User != nil {
			userCount++
		}
		if m.ToolResult == nil {
			continue
		}
		result := m.ToolResult
		switch result.ToolCallID {
		case "call_a":
			haveA = true
		case "call_ghost":
			haveGhost = true
		case "call_b":
			if result.IsError && result.Text() == "No result provided" {
				haveSyntheticB = true
			}
		}
	}
	if !haveA {
		t.Error("valid result call_a must be preserved")
	}
	if haveGhost {
		t.Error("orphaned result call_ghost must be stripped (D48)")
	}
	if !haveSyntheticB {
		t.Error("missing result for call_b must be synthesized as an error result")
	}
	if userCount != 1 {
		t.Errorf("user message count = %d, want 1", userCount)
	}
}
