package ai

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestContentBlocksUnmarshalClosedUnion(t *testing.T) {
	raw := []byte(`[
		{"type":"text","text":"hi","textSignature":"text-signature"},
		{"type":"image","mimeType":"image/png","data":"base64bytes"},
		{"type":"toolCall","id":"call-1","name":"bash","arguments":{"command":"ls"},"thoughtSignature":"thought-signature","namespace":"tools"},
		{"type":"thinking","thinking":"reasoning","thinkingSignature":"thinking-signature"}
	]`)
	var blocks ContentBlocks
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	want := ContentBlocks{
		TextContent{Text: "hi", TextSignature: "text-signature"},
		ImageContent{MimeType: "image/png", Data: "base64bytes"},
		ToolCall{ID: "call-1", Name: "bash", Arguments: JsonObject{"command": "ls"}, ThoughtSignature: "thought-signature", Namespace: "tools"},
		ThinkingContent{Thinking: "reasoning", ThinkingSignature: "thinking-signature"},
	}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", blocks, want)
	}
}

func TestContentBlocksRejectUnknownType(t *testing.T) {
	var blocks ContentBlocks
	err := json.Unmarshal([]byte(`[{"type":"future_block","payload":"x"}]`), &blocks)
	if err == nil {
		t.Fatal("unknown content block type succeeded")
	}
}

// Persisted messages must decode back to the same values so old sessions load.
func TestPersistedMessagesRoundTrip(t *testing.T) {
	roundTrip := func(message Message, role string) map[string]json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || string(fields["role"]) != `"`+role+`"` {
			t.Fatalf("%s wire = %s (%v)", role, raw, err)
		}
		return fields
	}
	user := UserContentBlocks{TextContent{Text: "read a.go"}, ImageContent{MimeType: "image/png", Data: "QUJD"}}
	var userBlocks ContentBlocks
	if err := json.Unmarshal(roundTrip(UserMessage{Content: user, Timestamp: 1}, "user")["content"], &userBlocks); err != nil || !reflect.DeepEqual(userBlocks, ContentBlocks{user[0], user[1]}) {
		t.Fatalf("user content = %#v (%v)", userBlocks, err)
	}
	assistant := AssistantMessage{Content: []AssistantContentBlock{
		ThinkingContent{Thinking: "plan", ThinkingSignature: "sig"},
		ToolCall{ID: "call-1", Name: "read", Arguments: JsonObject{"path": "a.go"}},
	}, API: APIAnthropicMessages, Provider: "anthropic", Model: "claude-test", StopReason: StopReasonToolUse, Timestamp: 2,
		Usage: Usage{Input: 10, Output: 5, TotalTokens: 15, Cost: UsageCost{Total: 0.01}}}
	raw, _ := json.Marshal(roundTrip(assistant, "assistant"))
	var decoded AssistantMessage
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, assistant) {
		t.Fatalf("assistant = %#v (%v), want %#v", decoded, err, assistant)
	}
	result := roundTrip(ToolResultMessage{ToolCallID: "call-1", ToolName: "read", Content: []ToolResultMessageContent{TextContent{Text: "package a"}}, IsError: true}, "toolResult")
	var resultBlocks ContentBlocks
	if err := json.Unmarshal(result["content"], &resultBlocks); err != nil || !reflect.DeepEqual(resultBlocks, ContentBlocks{TextContent{Text: "package a"}}) ||
		string(result["toolCallId"]) != `"call-1"` || string(result["isError"]) != "true" {
		t.Fatalf("tool result wire = %v (%v)", result, err)
	}
}
