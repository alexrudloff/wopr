package ai

import (
	"strings"
	"testing"
)

func TestSSEDecoderAcceptsSSELineEndingsAndDispatchesAtEOF(t *testing.T) {
	decoder := newSSEDecoder(strings.NewReader(": keepalive\rid: ignored\rdata:first\r\ndata: second\n\nevent:done\rdata:last"))
	if !decoder.Next() {
		t.Fatalf("first event missing: %v", decoder.Err())
	}
	first := decoder.Event()
	if first.Event != "" || first.Data != "first\nsecond" {
		t.Fatalf("first event = %#v", first)
	}
	if !decoder.Next() {
		t.Fatalf("EOF event missing: %v", decoder.Err())
	}
	second := decoder.Event()
	if second.Event != "done" || second.Data != "last" {
		t.Fatalf("second event = %#v", second)
	}
	if decoder.Next() || decoder.Err() != nil {
		t.Fatalf("decoder terminal state: err=%v", decoder.Err())
	}
}

func TestProvidersRejectMalformedSSEJSON(t *testing.T) {
	assertError := func(t *testing.T, result *AssistantMessage, want string) {
		t.Helper()
		if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, want) {
			t.Fatalf("result = %#v, want error containing %q", result, want)
		}
	}
	t.Run("anthropic", func(t *testing.T) {
		result := anthropicTerminal(t, runAnthropicSSE(t, "event: message_start\ndata: {\"message\":{\"usage\":{}}}\n\nevent: content_block_delta\ndata: {bad json\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{}}\n\nevent: message_stop\ndata: {}\n\n"))
		assertError(t, result, "Could not parse Anthropic SSE event content_block_delta")
	})
	t.Run("openai-completions", func(t *testing.T) {
		result := runOpenAICompletionsSSE(t, "data: {bad json\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		assertError(t, result, "invalid")
	})
	t.Run("openai-responses", func(t *testing.T) {
		result, _ := collectResponsesEvents(t, "data: {bad json\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		assertError(t, result, "invalid")
	})
}

func TestAnthropicSSERepairsRawControlCharacters(t *testing.T) {
	result := anthropicTerminal(t, runAnthropicSSE(t, "event: message_start\ndata: {\"message\":{\"usage\":{}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\tb\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{}}\n\nevent: message_stop\ndata: {}\n\n"))
	if result.StopReason != StopReasonStop || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "a\tb" {
		t.Fatalf("result = %#v", result)
	}
}
