package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// An empty tool result (a
// command that produced no output) must become the "(no tool output)"
// placeholder on the openai-responses function_call_output, never an empty
// string and never an image placeholder.
func TestResponsesConvertMessages_EmptyToolResultUsesPlaceholder(t *testing.T) {
	p := &openAIResponsesProvider{cfg: OpenAIResponsesConfig{ProviderID: "openai", Model: "gpt-4o-mini"}}
	messages := []Message{
		UserMessage{Content: UserText("Run the command")},
		AssistantMessage{Content: []AssistantContentBlock{
			ToolCall{ID: "tool-1", Name: "bash", Arguments: JsonObject{"command": "true"}},
		}},
		ToolResultMessage{ToolCallID: "tool-1", ToolName: "bash", Content: []ToolResultMessageContent{TextContent{}}},
	}

	items, _ := p.convertMessages(messages, nil)

	var fco *respInputItem
	for i := range items {
		if items[i].Type == "function_call_output" {
			fco = &items[i]
		}
	}
	if fco == nil {
		t.Fatal("no function_call_output item produced")
	}
	var got string
	if err := json.Unmarshal(fco.Output, &got); err != nil {
		t.Fatalf("output is not a JSON string: %v (raw %s)", err, fco.Output)
	}
	if got != "(no tool output)" {
		t.Errorf("empty tool-result output = %q, want %q", got, "(no tool output)")
	}
	if strings.Contains(got, "see attached image") {
		t.Errorf("empty tool-result output %q must not mention an image", got)
	}
}
