package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func assertWireStopReason(t *testing.T, stream *AssistantMessageEventStream, want StopReason) {
	t.Helper()
	result := stream.Result()
	if result.StopReason != want {
		t.Fatalf("message stop reason = %q, want %q", result.StopReason, want)
	}
	if len(result.Content) != 1 {
		t.Fatalf("message content = %#v, want one tool call", result.Content)
	}
	if _, ok := result.Content[0].(ToolCall); !ok {
		t.Fatalf("message content[0] = %T, want ToolCall", result.Content[0])
	}
	var terminal StopReason
	for event := range stream.Events(context.Background()) {
		if done, ok := event.(DoneEvent); ok {
			terminal = done.Reason
		}
	}
	if terminal != want {
		t.Fatalf("terminal stop reason = %q, want %q", terminal, want)
	}
}

// OpenAI Completions maps the provider finish reason directly even
// when the streamed content contains tool calls. The agent loop independently
// derives continuation from that content.
func TestOpenAICompletionsWireToolCallsPreserveStop(t *testing.T) {
	for _, providerID := range []string{"openai"} {
		for _, finishReason := range []string{"stop"} {
			t.Run(providerID+"/"+finishReason, func(t *testing.T) {
				wire := fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":%q}]}

`, finishReason) +
					"data: [DONE]\n\n"
				provider := &openAIProvider{}
				builder := newAssistantStreamBuilder(context.Background(), APIOpenAICompletions, providerID, "model", ModelCost{})
				provider.parseSSE(context.Background(), strings.NewReader(wire), builder, nil)
				assertWireStopReason(t, builder.stream, StopReasonStop)
			})
		}
	}
}

// OpenAI Responses is shared by stock OpenAI and
// Codex. Each route must preserve the function-call-over-completed rule.
func TestOpenAIResponsesWireToolCallsOverrideCompleted(t *testing.T) {
	tests := []struct {
		name       string
		api        API
		providerID string
	}{
		{name: "openai", api: APIOpenAIResponses, providerID: "openai"},
		{name: "codex", api: APIOpenAICodexResponses, providerID: "openai-codex"},
	}
	for _, test := range tests {
		for _, status := range []string{"", "completed"} {
			name := status
			if name == "" {
				name = "missing-status"
			}
			t.Run(test.name+"/"+name, func(t *testing.T) {
				wire := fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"read","arguments":"{}"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"read","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"response-1","status":%q}}

`, status)
				provider := &openAIResponsesProvider{cfg: OpenAIResponsesConfig{ProviderID: test.providerID, Model: "model"}}
				builder := newAssistantStreamBuilder(context.Background(), test.api, test.providerID, "model", ModelCost{})
				provider.parseResponsesSSE(context.Background(), strings.NewReader(wire), builder, nil)
				assertWireStopReason(t, builder.stream, StopReasonToolUse)
			})
		}
	}
}

func TestAnthropicWireToolCallsPreserveSuccessfulStopReason(t *testing.T) {
	for _, providerID := range []string{"anthropic"} {
		for _, stopReason := range []string{"end_turn", "pause_turn"} {
			t.Run(providerID+"/"+stopReason, func(t *testing.T) {
				wire := fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"message-1","model":"claude-test","usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"read","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`, stopReason)
				provider := &anthropicProvider{cfg: AnthropicConfig{ProviderID: providerID, Model: "claude-test"}}
				builder := newAssistantStreamBuilder(context.Background(), APIAnthropicMessages, providerID, "claude-test", ModelCost{})
				provider.parseAnthropicSSE(context.Background(), strings.NewReader(wire), builder, anthropicStreamNames{})
				assertWireStopReason(t, builder.stream, StopReasonStop)
			})
		}
	}
}
