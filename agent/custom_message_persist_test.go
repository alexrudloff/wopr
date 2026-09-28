package agent

import (
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// The delivered order is what gets recorded: a follow-up queued mid-turn is
// persisted after the tool_result that was already in flight, never between the
// tool_use and its result.
func TestFollowUpCustomMessagePersistsAfterToolResult(t *testing.T) {
	var order []string
	a := NewAgent(AgentOptions{
		OnMessagePersist: func(m AgentMessage) error {
			switch {
			case m.Assistant != nil:
				order = append(order, "assistant")
			case m.ToolResult != nil:
				order = append(order, "toolResult")
			case m.Custom != nil:
				order = append(order, "custom")
			}
			return nil
		},
	})

	// The sequence the agent delivers: assistant tool_use, its result, then the
	// queued custom message drained afterwards.
	if err := a.persistMessage(AgentMessage{Assistant: &AssistantMessage{
		Role:    RoleAssistant,
		Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "call-1", Name: "bash"}},
	}}); err != nil {
		t.Fatalf("persist assistant message: %v", err)
	}
	if err := a.persistMessage(AgentMessage{ToolResult: &ToolResultMessage{
		Role: RoleToolResult, ToolCallID: "call-1",
	}}); err != nil {
		t.Fatalf("persist tool result: %v", err)
	}
	if err := a.persistMessage(AgentMessage{Custom: map[string]any{
		"role": RoleCustom, "customType": "chain-completion", "content": "done",
	}}); err != nil {
		t.Fatalf("persist custom message: %v", err)
	}

	want := []string{"assistant", "toolResult", "custom"}
	if len(order) != len(want) {
		t.Fatalf("persist order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("persist order = %v, want %v", order, want)
		}
	}
}
