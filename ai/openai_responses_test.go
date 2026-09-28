package ai

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type responsesTestRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip responsesTestRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func collectResponsesEvents(t *testing.T, sse string) (*AssistantMessage, []AssistantMessageEvent) {
	t.Helper()
	provider := &openAIResponsesProvider{}
	builder := newAssistantStreamBuilder(context.Background(), APIOpenAIResponses, "openai", "model", ModelCost{})
	go provider.parseResponsesSSE(context.Background(), strings.NewReader(sse), builder, nil)
	result := builder.stream.Result()
	var events []AssistantMessageEvent
	for event := range builder.stream.Events(context.Background()) {
		events = append(events, event)
	}
	return result, events
}

func TestResponsesSSE_TextStreaming(t *testing.T) {
	sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","content":[]}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello"}

data: {"type":"response.output_text.delta","output_index":0,"delta":" world"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"Hello world"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}

`
	result, _ := collectResponsesEvents(t, sse)
	if result.Content[0].(TextContent).Text != "Hello world" || result.Usage.Input != 10 || result.Usage.Output != 5 {
		t.Fatalf("result = %#v", result)
	}
}

func TestResponsesSSE_ToolCall(t *testing.T) {
	sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"main.go\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"read","arguments":"{\"path\":\"main.go\"}"}}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	result, _ := collectResponsesEvents(t, sse)
	tool := result.Content[0].(ToolCall)
	if tool.ID != "call_abc|fc_1" || tool.Arguments["path"] != "main.go" || result.StopReason != StopReasonToolUse {
		t.Fatalf("result = %#v", result)
	}
}

func TestResponsesSSE_ThinkingStream(t *testing.T) {
	sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r1"}}

data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"think"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"r1","summary":[{"text":"think"}]}}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	result, _ := collectResponsesEvents(t, sse)
	if result.Content[0].(ThinkingContent).Thinking != "think" {
		t.Fatalf("result = %#v", result)
	}
}

func TestResponsesSSE_ErrorEvent(t *testing.T) {
	result, _ := collectResponsesEvents(t, `data: {"type":"error","code":"bad","message":"failed"}

`)
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "bad") {
		t.Fatalf("result = %#v", result)
	}
}

func TestResponsesSSE_FailedResponse(t *testing.T) {
	result, _ := collectResponsesEvents(t, `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server","message":"down"}}}

`)
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "server: down") {
		t.Fatalf("result = %#v", result)
	}
}

func TestResponsesSSE_CachedTokens(t *testing.T) {
	result, _ := collectResponsesEvents(t, `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,"input_tokens_details":{"cached_tokens":80}}}}

`)
	if result.Usage.Input != 20 || result.Usage.CacheRead != 80 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

// Replayed reasoning precedes its call and keeps the call's fc_ id; without
// it (a trailing orphan is trimmed) the call goes without its item id, which
// the API would otherwise reject as missing its reasoning.
func TestResponsesConvertMessages_AssistantToolCall(t *testing.T) {
	provider := &openAIResponsesProvider{}
	reasoning := ThinkingContent{Thinking: "t", ThinkingSignature: `{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}`}
	call := ToolCall{ID: "call_1|fc_1", Name: "read", Arguments: JsonObject{"path": "x.go"}}
	for _, tc := range []struct {
		name    string
		content []AssistantContentBlock
		want    string
	}{
		{"with reasoning", []AssistantContentBlock{reasoning, TextContent{Text: "Let me check."}, call}, "reasoning:rs_1 message:msg_pi_0 function_call:fc_1"},
		{"reasoning dropped", []AssistantContentBlock{TextContent{Text: "Let me check."}, call}, "message:msg_pi_0 function_call:"},
		{"orphan reasoning", []AssistantContentBlock{TextContent{Text: "x"}, reasoning}, "message:msg_pi_0"},
	} {
		items, _ := provider.convertMessages([]Message{AssistantMessage{Content: tc.content}}, nil)
		got := make([]string, len(items))
		for i, item := range items {
			got[i] = item.Type + ":" + item.ID
		}
		if strings.Join(got, " ") != tc.want || (len(items) > 0 && items[0].Type == "reasoning" && string(items[0].EncryptedContent) != `"ENC"`) {
			t.Errorf("%s: items = %v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestResponsesConvertMessages_ToolResult(t *testing.T) {
	provider := &openAIResponsesProvider{}
	items, _ := provider.convertMessages([]Message{ToolResultMessage{ToolCallID: "call_1|fc_1", Content: []ToolResultMessageContent{TextContent{Text: "file contents"}}}}, nil)
	if len(items) != 1 || items[0].Type != "function_call_output" || items[0].CallID != "call_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestResponsesConvertMessages_CrossProviderThinkingSkipped(t *testing.T) {
	provider := &openAIResponsesProvider{cfg: OpenAIResponsesConfig{ProviderID: "openai", Model: "gpt-5"}}
	items, _ := provider.convertMessages([]Message{AssistantMessage{Provider: "anthropic", Model: "claude", Content: []AssistantContentBlock{
		ThinkingContent{Thinking: "reasoning", ThinkingSignature: "opaque"}, TextContent{Text: "answer"},
	}}}, nil)
	if len(items) != 1 || items[0].Type != "message" {
		t.Fatalf("items = %#v", items)
	}
}

func TestOpenAIResponsesDoneBeforeTerminalEventFails(t *testing.T) {
	result, _ := collectResponsesEvents(t, "data: [DONE]\n\n")
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "ended before a terminal response event") || !IsRetryableAssistantError(*result) {
		t.Fatalf("result = %#v, want retryable early-end error", result)
	}
}
