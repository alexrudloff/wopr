package ai

import (
	"math"
	"testing"
)

func TestNormalizeContextRejectsInvalidJSONValues(t *testing.T) {
	tests := []struct {
		name      string
		arguments JsonObject
	}{
		{name: "non-finite", arguments: JsonObject{"value": math.Inf(1)}},
		{name: "unsupported", arguments: JsonObject{"value": make(chan int)}},
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	tests = append(tests, struct {
		name      string
		arguments JsonObject
	}{name: "cycle", arguments: JsonObject(cycle)})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context := NormalizeContext(Context{Messages: []Message{AssistantMessage{Content: []AssistantContentBlock{
				ToolCall{ID: "call", Name: "tool", Arguments: test.arguments},
			}}}})
			if context.err == nil {
				t.Fatal("NormalizeContext accepted invalid JSON")
			}
			if err := validateTranscriptContext(context); err == nil {
				t.Fatal("provider boundary accepted invalid transcript")
			}
		})
	}
}

func TestResolveTranscriptCollapsesUnsupportedMidConversationSystemMessages(t *testing.T) {
	ctx := newTranscriptContext([]Message{SystemMessage{Content: SystemText("base"), Timestamp: 1}, UserMessage{Content: UserText("question"), Timestamp: 2}, SystemMessage{Content: SystemText("later"), Timestamp: 3}, AssistantMessage{Content: []AssistantContentBlock{TextContent{Text: "answer"}}, StopReason: StopReasonStop, Timestamp: 4}})
	preserved := ResolveTranscript(ctx, true).Messages()
	if len(preserved) != 4 {
		t.Fatalf("preserved = %#v", preserved)
	}
	collapsed := ResolveTranscript(ctx, false).Messages()
	head, ok := collapsed[0].(SystemMessage)
	if len(collapsed) != 3 || !ok || head.Content != SystemText("base\n\nlater") {
		t.Fatalf("collapsed = %#v", collapsed)
	}
}
