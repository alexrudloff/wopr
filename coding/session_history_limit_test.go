package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

func firstNonSystem(messages []agent.AgentMessage) agent.AgentMessage {
	for _, message := range messages {
		if message.System == nil {
			return message
		}
	}
	return agent.AgentMessage{}
}

func hasToolCall(messages []agent.AgentMessage, id string) bool {
	for _, message := range messages {
		if message.Assistant == nil {
			continue
		}
		for _, block := range message.Assistant.Content {
			if call, ok := block.(ai.ToolCall); ok && call.ID == id {
				return true
			}
		}
	}
	return false
}

func hasToolResult(messages []agent.AgentMessage, id string) bool {
	for _, message := range messages {
		if message.ToolResult != nil && message.ToolResult.ToolCallID == id {
			return true
		}
	}
	return false
}

func TestMaxHistoryTokensTrimsOldest(t *testing.T) {
	const budget = 60
	big := strings.Repeat("x", 4000)

	largeUser := func() agent.AgentMessage {
		return agent.AgentMessage{User: &agent.UserMessage{
			Role:    agent.RoleUser,
			Content: []ai.UserContentBlock{ai.TextContent{Text: big}},
		}}
	}
	smallUser := func(text string) agent.AgentMessage {
		return agent.AgentMessage{User: &agent.UserMessage{
			Role:    agent.RoleUser,
			Content: []ai.UserContentBlock{ai.TextContent{Text: text}},
		}}
	}
	toolCall := func(id string) agent.AgentMessage {
		return agent.AgentMessage{Assistant: &agent.AssistantMessage{
			Role:       agent.RoleAssistant,
			StopReason: ai.StopReasonToolUse,
			Content:    []ai.AssistantContentBlock{ai.ToolCall{ID: id, Name: "read", Arguments: ai.JsonObject{}}},
		}}
	}
	systemBaseline := func() agent.AgentMessage {
		return agent.AgentMessage{System: &ai.SystemMessage{Content: ai.SystemText("you are a test"), Timestamp: 1}}
	}
	toolResult := func(id, text string) agent.AgentMessage {
		return agent.AgentMessage{ToolResult: &agent.ToolResultMessage{
			Role:       agent.RoleToolResult,
			ToolCallID: id,
			ToolName:   "read",
			Content:    []ai.ToolResultMessageContent{ai.TextContent{Text: text}},
		}}
	}

	newSession := func(t *testing.T) *Session {
		t.Helper()
		svcs, err := NewServices(ServicesOptions{CWD: t.TempDir(), MaxHistoryTokens: new(budget)})
		if err != nil {
			t.Fatalf("NewServices: %v", err)
		}
		sess, err := NewSession(svcs, SessionOptions{Model: fakeModel(), SystemPrompt: "you are a test"})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}
	appendMessage := func(t *testing.T, sess *Session, message agent.AgentMessage) {
		t.Helper()
		if _, err := sess.inner.AppendMessage(message); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	t.Run("budget holds", func(t *testing.T) {
		sess := newSession(t)
		appendMessage(t, sess, systemBaseline())
		for range 3 {
			appendMessage(t, sess, largeUser())
		}
		appendMessage(t, sess, smallUser("hi"))
		appendMessage(t, sess, toolCall("call-1"))
		appendMessage(t, sess, toolResult("call-1", "ok"))
		sess.RefreshContext()

		update := sess.prepareRequest(context.Background(), agent.PrepareRequestContext{})
		trimmed := update.Context
		if got := compaction.EstimateMessagesTokens(trimmed); got > budget {
			t.Fatalf("trimmed context = %d tokens, want <= %d", got, budget)
		}
		if len(trimmed) == 0 || trimmed[0].System == nil {
			t.Fatalf("leading system baseline missing")
		}
		if text, ok := trimmed[0].System.Content.(ai.SystemText); !ok || string(text) != "you are a test" {
			t.Fatalf("leading message is not the instruction baseline: %#v", trimmed[0].System.Content)
		}
		for _, message := range trimmed {
			if message.User != nil && len(message.User.Content) > 0 {
				if text, ok := message.User.Content[0].(ai.TextContent); ok && text.Text == big {
					t.Fatalf("oldest large message survived the trim")
				}
			}
		}
		first := firstNonSystem(trimmed)
		if first.User == nil {
			t.Fatalf("kept suffix does not begin at the recent user message: role=%q", first.Role())
		}
	})

	t.Run("over-budget tail kept", func(t *testing.T) {
		sess := newSession(t)
		appendMessage(t, sess, systemBaseline())
		appendMessage(t, sess, largeUser())
		appendMessage(t, sess, smallUser("hello"))
		appendMessage(t, sess, toolCall("call-2"))
		appendMessage(t, sess, toolResult("call-2", big))
		sess.RefreshContext()

		update := sess.prepareRequest(context.Background(), agent.PrepareRequestContext{})
		trimmed := update.Context
		if !hasToolCall(trimmed, "call-2") {
			t.Fatalf("final assistant tool call dropped")
		}
		if !hasToolResult(trimmed, "call-2") {
			t.Fatalf("final tool result dropped")
		}
		if role := firstNonSystem(trimmed).Role(); role == agent.RoleToolResult {
			t.Fatalf("context begins with a tool result; pairing broken")
		}
	})
}
