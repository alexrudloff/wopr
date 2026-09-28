package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// helper to create a test SSE server and run the provider against it.
func runAnthropicSSE(t *testing.T, sseData string) []AssistantMessageEvent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, sseData)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropicProvider(AnthropicConfig{
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-20250514",
		BaseURL:    srv.URL,
		ProviderID: "anthropic",
	})

	transcript := NormalizeContext(Context{Messages: []Message{
		SystemMessage{Content: SystemText("You are helpful")},
		UserMessage{Content: UserText("Hello")},
	}})
	stream, err := p.Stream(context.Background(), transcript, StreamOptions{MaxTokens: 1024})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var events []AssistantMessageEvent
	for event := range stream.Events(context.Background()) {
		events = append(events, event)
	}
	return events
}

func anthropicEventTypes(events []AssistantMessageEvent) []AssistantEventType {
	types := make([]AssistantEventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.EventType())
	}
	return types
}

func anthropicTerminal(t *testing.T, events []AssistantMessageEvent) *AssistantMessage {
	t.Helper()
	for _, event := range events {
		switch event := event.(type) {
		case DoneEvent:
			return event.Message
		case ErrorEvent:
			return event.Error
		}
	}
	t.Fatal("Anthropic stream has no terminal event")
	return nil
}

func TestAnthropicSSE_TextOnly(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":25,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world!"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`
	events := runAnthropicSSE(t, sse)

	// Expect: start, text(""), text("Hello"), text(" world!"), done
	var texts []string
	var gotStart, gotDone bool
	for _, event := range events {
		switch event := event.(type) {
		case StartEvent:
			gotStart = true
		case TextDeltaEvent:
			texts = append(texts, event.Delta)
		case DoneEvent:
			gotDone = true
			if event.Message.Usage.Input != 25 {
				t.Errorf("input = %d, want 25", event.Message.Usage.Input)
			}
			if event.Message.Usage.Output != 12 {
				t.Errorf("output = %d, want 12", event.Message.Usage.Output)
			}
		}
	}
	if !gotStart {
		t.Error("missing start event")
	}
	if !gotDone {
		t.Error("missing done event")
	}
	joined := strings.Join(texts, "")
	if !strings.Contains(joined, "Hello world!") {
		t.Errorf("text deltas = %q, want to contain 'Hello world!'", joined)
	}
}

func TestAnthropicSSE_ToolCallNoDoubleArgsOnStop(t *testing.T) {
	// Regression: content_block_stop for tool_use previously re-emitted
	// ab.toolArgs.String() as an ArgsDelta, which the agent loop appended
	// to the accumulated args, producing `{...}{...}` JSON that failed
	// parsing. Finalization now happens in place with no synthetic
	// delta. This test asserts the concatenated ArgsDelta equals the
	// original streamed JSON: not doubled.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_dup","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_dup","name":"bash"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{}}

event: message_stop
data: {"type":"message_stop"}

`
	events := runAnthropicSSE(t, sse)
	message := anthropicTerminal(t, events)
	tool := message.Content[0].(ToolCall)
	if tool.Arguments["command"] != "ls" {
		t.Fatalf("tool = %#v", tool)
	}
	wantTypes := []AssistantEventType{EventStart, EventToolCallStart, EventToolCallDelta, EventToolCallEnd, EventDone}
	if got := anthropicEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	delta := events[2].(ToolCallDeltaEvent)
	if delta.ContentIndex != 0 || delta.Delta != `{"command": "ls"}` {
		t.Fatalf("tool delta = %#v", delta)
	}
	end := events[3].(ToolCallEndEvent)
	if end.ContentIndex != 0 || !reflect.DeepEqual(end.ToolCall.Arguments, JsonObject{"command": "ls"}) {
		t.Fatalf("tool end = %#v", end)
	}
}

func TestAnthropicSSE_ParallelToolCallsTrackIndex(t *testing.T) {
	// Regression: Anthropic stream previously left Index=0 on every
	// ToolCallDelta, causing parallel tool calls to collide in the agent's
	// toolMap (wrong tool name paired with wrong args). This test
	// asserts each tool block carries its Anthropic content-block index.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_par","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"bash"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{}}

event: message_stop
data: {"type":"message_stop"}

`
	events := runAnthropicSSE(t, sse)
	message := anthropicTerminal(t, events)
	if len(message.Content) != 2 {
		t.Fatalf("content = %#v", message.Content)
	}
	first := message.Content[0].(ToolCall)
	second := message.Content[1].(ToolCall)
	if first.Name != "read" || first.Arguments["path"] != "a" || second.Name != "bash" || second.Arguments["command"] != "ls" {
		t.Fatalf("content = %#v", message.Content)
	}
	wantTypes := []AssistantEventType{
		EventStart,
		EventToolCallStart, EventToolCallDelta, EventToolCallEnd,
		EventToolCallStart, EventToolCallDelta, EventToolCallEnd,
		EventDone,
	}
	if got := anthropicEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	firstDelta := events[2].(ToolCallDeltaEvent)
	secondDelta := events[5].(ToolCallDeltaEvent)
	if firstDelta.ContentIndex != 0 || firstDelta.Delta != `{"path":"a"}` || secondDelta.ContentIndex != 1 || secondDelta.Delta != `{"command":"ls"}` {
		t.Fatalf("tool deltas = %#v, %#v", firstDelta, secondDelta)
	}
}

func TestAnthropicSSE_Thinking(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_789","usage":{"input_tokens":30,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is my answer."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`
	message := anthropicTerminal(t, runAnthropicSSE(t, sse))
	thinking := message.Content[0].(ThinkingContent)
	text := message.Content[1].(TextContent)
	if thinking.Thinking != "Let me think..." || thinking.ThinkingSignature != "sig123" || text.Text != "Here is my answer." {
		t.Fatalf("content = %#v", message.Content)
	}
}

func TestAnthropicSSE_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropicProvider(AnthropicConfig{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: srv.URL,
	})

	_, err := p.Stream(context.Background(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("Hi")}}}), StreamOptions{MaxTokens: 1024})
	if err == nil {
		t.Fatal("expected error for 529 response")
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error = %q, want to contain 'overloaded_error'", err.Error())
	}
	if !strings.Contains(err.Error(), "Overloaded") {
		t.Errorf("error = %q, want to contain 'Overloaded'", err.Error())
	}
}

func TestAnthropicSSE_Usage(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_u","usage":{"input_tokens":100,"output_tokens":0,"cache_read_input_tokens":50,"cache_creation_input_tokens":10,"output_tokens_details":{"thinking_tokens":3}}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"output_tokens_details":{"thinking_tokens":4}}}

event: message_stop
data: {"type":"message_stop"}

`
	usage := anthropicTerminal(t, runAnthropicSSE(t, sse)).Usage
	if usage.Input != 100 {
		t.Errorf("input = %d, want 100", usage.Input)
	}
	if usage.Output != 5 {
		t.Errorf("output = %d, want 5", usage.Output)
	}
	if usage.CacheRead != 50 {
		t.Errorf("cache_read = %d, want 50", usage.CacheRead)
	}
	if usage.CacheWrite != 10 {
		t.Errorf("cache_write = %d, want 10", usage.CacheWrite)
	}
	if usage.Reasoning == nil || *usage.Reasoning != 4 {
		t.Errorf("reasoning = %v, want 4", usage.Reasoning)
	}
}

// Thinking-block conversion rules:
// redacted -> redacted_thinking, empty-text -> dropped, empty/invalid signature
// -> text (or preserved thinking when allowEmptySignature), else thinking+sig.
// The empty-text+signature case is the github-copilot/claude regression:
// thinkingDisplay "omitted" persists an empty `thinking` field with a valid
// signature, and re-sending it verbatim yields HTTP 400
// "messages.N.content.0.thinking.thinking: Field required".
func TestAnthConvertMessages_ThinkingBlocks(t *testing.T) {
	const sig = "EpQMCmMIDxgCKkABCDEF" // valid-looking Anthropic signature (not JSON)
	type want struct {
		dropped bool // assistant message produces no output block
		block   anthContentBlock
	}
	tests := []struct {
		name                string
		in                  ThinkingContent
		allowEmptySignature bool
		want                want
	}{
		{
			name: "empty thinking with signature is preserved",
			in:   ThinkingContent{Thinking: "", ThinkingSignature: sig},
			want: want{block: anthContentBlock{Type: "thinking", Thinking: "", Signature: sig}},
		},
		{
			name: "whitespace-only thinking with signature is preserved",
			in:   ThinkingContent{Thinking: "   \n", ThinkingSignature: sig},
			want: want{block: anthContentBlock{Type: "thinking", Thinking: "   \n", Signature: sig}},
		},
		{
			name: "empty thinking with empty signature is dropped",
			in:   ThinkingContent{Thinking: "", ThinkingSignature: ""},
			want: want{dropped: true},
		},
		{
			name: "redacted block becomes redacted_thinking carrying the signature",
			in:   ThinkingContent{Thinking: "[Reasoning redacted]", ThinkingSignature: "opaque-data", Redacted: true},
			want: want{block: anthContentBlock{Type: "redacted_thinking", Data: "opaque-data"}},
		},
		{
			name: "normal thinking with signature is preserved",
			in:   ThinkingContent{Thinking: "reasoning", ThinkingSignature: sig},
			want: want{block: anthContentBlock{Type: "thinking", Thinking: "reasoning", Signature: sig}},
		},
		{
			name: "thinking with empty signature converts to text by default",
			in:   ThinkingContent{Thinking: "reasoning", ThinkingSignature: ""},
			want: want{block: anthContentBlock{Type: "text", Text: "reasoning"}},
		},
		{
			name:                "thinking with empty signature preserved when allowEmptySignature",
			in:                  ThinkingContent{Thinking: "reasoning", ThinkingSignature: ""},
			allowEmptySignature: true,
			want:                want{block: anthContentBlock{Type: "thinking", Thinking: "reasoning", Signature: ""}},
		},
		// Cross-model thinking (e.g. an OpenAI reasoning item whose signature is a
		// JSON object) is converted to text upstream of this converter, in
		// agent.NormalizeMessages. See
		// TestNormalizeMessages_CrossModelThinking for that behavior; the
		// converter only ever sees same-model thinking.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := []Message{AssistantMessage{Content: []AssistantContentBlock{tt.in}}}
			out := anthConvertMessages(messages, false, tt.allowEmptySignature, false)
			if tt.want.dropped {
				if len(out) != 0 {
					t.Fatalf("want assistant message dropped, got %d messages: %+v", len(out), out)
				}
				return
			}
			if len(out) != 1 {
				t.Fatalf("want 1 message, got %d: %+v", len(out), out)
			}
			blocks, ok := out[0].Content.([]anthContentBlock)
			if !ok || len(blocks) != 1 {
				t.Fatalf("want 1 content block, got %#v", out[0].Content)
			}
			if blocks[0] != tt.want.block {
				t.Errorf("block = %#v, want %#v", blocks[0], tt.want.block)
			}
		})
	}
}

func TestNormalizeAnthropicToolCallID(t *testing.T) {
	// Replace chars outside [a-zA-Z0-9_-] with "_", then truncate to 64.
	tests := []struct {
		in, want string
	}{
		{"toolu_abc123", "toolu_abc123"},                    // already valid
		{"call_abc123", "call_abc123"},                      // OpenAI completions format
		{"call_abc|item_def", "call_abc_item_def"},          // OpenAI responses pipe
		{"a.b:c/d", "a_b_c_d"},                              // various invalid chars
		{strings.Repeat("x", 100), strings.Repeat("x", 64)}, // truncate to 64
		{"", ""}, // empty
	}
	for _, tt := range tests {
		got := normalizeAnthropicToolCallID(tt.in)
		if got != tt.want {
			t.Errorf("normalizeAnthropicToolCallID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAnthropicConvertMessages_NormalizesToolCallIDs(t *testing.T) {
	// Cross-provider scenario: OpenAI Responses API tool call IDs contain pipes.
	// The Anthropic API requires ^[a-zA-Z0-9_-]+$.
	messages := []Message{
		AssistantMessage{Content: []AssistantContentBlock{ToolCall{ID: "call_abc|item_def", Name: "read", Arguments: JsonObject{}}}},
		ToolResultMessage{ToolCallID: "call_abc|item_def", Content: []ToolResultMessageContent{TextContent{Text: "ok"}}},
	}
	out := anthConvertMessages(messages, false, false, false)
	if len(out) != 2 {
		t.Fatalf("got %d messages, want 2", len(out))
	}
	// Assistant message tool_use block should have normalized ID.
	blocks := out[0].Content.([]anthContentBlock)
	if blocks[0].ID != "call_abc_item_def" {
		t.Errorf("tool_use ID = %q, want %q", blocks[0].ID, "call_abc_item_def")
	}
	// User message tool_result block should have matching normalized ID.
	results := out[1].Content.([]anthContentBlock)
	if results[0].ToolUseID != "call_abc_item_def" {
		t.Errorf("tool_result tool_use_id = %q, want %q", results[0].ToolUseID, "call_abc_item_def")
	}
}

// TestAnthropicConvertToolsStrict covers the strict-versus-legacy schema cases.
func TestAnthropicConvertToolsStrict(t *testing.T) {
	legacy := ToolSchema{
		Name: "lookup", Description: "Look up a value",
		Parameters: map[string]any{
			"type": "object", "title": "LookupInput", "additionalProperties": false,
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []any{"value"},
		},
	}
	legacyTools, err := anthConvertTools([]ToolSchema{legacy}, false, true, true, nil)
	if err != nil {
		t.Fatalf("legacy tool: %v", err)
	}
	if legacyTools[0].Strict != nil {
		t.Fatalf("legacy strict = %#v, want omitted", legacyTools[0].Strict)
	}
	if _, exists := legacyTools[0].InputSchema["title"]; exists {
		t.Fatalf("legacy input schema retained title: %#v", legacyTools[0].InputSchema)
	}
	if _, exists := legacyTools[0].InputSchema["additionalProperties"]; exists {
		t.Fatalf("legacy input schema retained additionalProperties: %#v", legacyTools[0].InputSchema)
	}

	strict := legacy
	strict.Parameters = map[string]any{
		"type": "object", "title": "StrictLookupInput",
		"properties": map[string]any{
			"value":    map[string]any{"type": "string"},
			"optional": map[string]any{"type": "number"},
		},
		"required": []any{"value"},
	}
	strict.ConstrainedSampling = &ConstrainedSamplingConfig{Type: "json_schema", Strict: "prefer"}
	strictTools, err := anthConvertTools([]ToolSchema{strict}, false, true, true, nil)
	if err != nil {
		t.Fatalf("strict tool: %v", err)
	}
	if strictTools[0].Strict == nil || !*strictTools[0].Strict {
		t.Fatalf("strict = %#v, want true", strictTools[0].Strict)
	}
	if strictTools[0].InputSchema["title"] != "StrictLookupInput" || strictTools[0].InputSchema["additionalProperties"] != false {
		t.Fatalf("strict input schema = %#v", strictTools[0].InputSchema)
	}
	optional := strictTools[0].InputSchema["properties"].(map[string]any)["optional"].(map[string]any)
	if _, ok := optional["anyOf"]; !ok {
		t.Fatalf("strict optional property = %#v, want nullable anyOf", optional)
	}
}

func TestAnthropicStreamSessionAffinityAndToolCacheControl(t *testing.T) {
	var gotHeader string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-session-affinity")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	t.Setenv("WOPR_CACHE_RETENTION", "short")
	sendAffinity := true
	supportsToolCache := false
	p := NewAnthropicProvider(AnthropicConfig{
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-20250514",
		BaseURL:    srv.URL,
		ProviderID: "fireworks",
		Compat: &ModelCompat{
			SendSessionAffinityHeaders:  &sendAffinity,
			SupportsCacheControlOnTools: &supportsToolCache,
		},
	})

	transcript := NormalizeContext(Context{Messages: []Message{
		SystemMessage{ToolsAdded: []ToolSchema{{Name: "bash", Description: "run bash", Parameters: map[string]any{"type": "object"}}}},
		UserMessage{Content: UserText("Hello")},
	}})
	stream, err := p.Stream(context.Background(), transcript, StreamOptions{SessionID: "sess-123"})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	for range stream.Events(context.Background()) {
	}
	if gotHeader != "sess-123" {
		t.Fatalf("x-session-affinity = %q, want sess-123", gotHeader)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool = %#v", tools[0])
	}
	if _, ok := tool["cache_control"]; ok {
		t.Fatalf("tool cache_control should be omitted, got %#v", tool["cache_control"])
	}
}

func TestAnthropicSSE_MissingMessageStopIsError(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_partial","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

`
	result := anthropicTerminal(t, runAnthropicSSE(t, sse))
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "Anthropic stream ended before message_stop") {
		t.Fatalf("result = %#v", result)
	}
}

// TestAnthropicStream_D37_RetriesOnStaleThinkingSignature is the regression
// guard for the D37 thinking-signature recovery. When the provider rejects a
// replayed thinking-block signature (a stale signature the backend no longer
// accepts: observed live on github-copilot claude after rewinds/aging), wopr
// must retry once with thinking signatures stripped rather than surfacing a
// hard error that wedges the session. The retry must preserve the reasoning
// text as a plain text block and drop only the signature.
func TestAnthropicStream_D37_RetriesOnStaleThinkingSignature(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid `+"`signature`"+` in `+"`thinking`"+` block"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"id":"m","usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropicProvider(AnthropicConfig{
		APIKey:     "test-key",
		Model:      "claude-opus-4.8",
		BaseURL:    srv.URL,
		ProviderID: "github-copilot",
	})

	messages := []Message{
		UserMessage{Content: UserText("hi")},
		AssistantMessage{Provider: "github-copilot", Model: "claude-opus-4.8", Content: []AssistantContentBlock{
			ThinkingContent{Thinking: "let me reason about this", ThinkingSignature: "STALE_SIG_ABC"},
			TextContent{Text: "answer"},
		}},
		UserMessage{Content: UserText("again")},
	}

	stream, err := p.Stream(context.Background(), NormalizeContext(Context{Messages: messages}), StreamOptions{MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	result := stream.Result()
	if len(result.Content) != 1 || result.Content[0].(TextContent).Text != "ok" {
		t.Fatalf("result = %#v", result)
	}

	if len(bodies) != 2 {
		t.Fatalf("expected exactly 2 requests (fail then retry), got %d", len(bodies))
	}
	// First attempt carries the signed thinking block.
	if !strings.Contains(bodies[0], "STALE_SIG_ABC") {
		t.Fatalf("first request should replay the thinking signature; body:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], `"type":"thinking"`) {
		t.Fatalf("first request should contain a thinking block; body:\n%s", bodies[0])
	}
	// Retry must strip the signature and downgrade thinking to text, preserving
	// the reasoning content.
	if strings.Contains(bodies[1], "STALE_SIG_ABC") {
		t.Fatalf("retry must not replay the stale signature; body:\n%s", bodies[1])
	}
	if strings.Contains(bodies[1], `"type":"thinking"`) {
		t.Fatalf("retry must not send a thinking block; body:\n%s", bodies[1])
	}
	if !strings.Contains(bodies[1], "let me reason about this") {
		t.Fatalf("retry must preserve reasoning text as text; body:\n%s", bodies[1])
	}
}

// TestIsThinkingSignatureError checks the D37 error classifier fires only on a
// thinking-block signature error, not on unrelated signature or thinking errors.
func TestIsThinkingSignatureError(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"thinking signature", `{"error":{"type":"invalid_request_error","message":"Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`, true},
		{"tool_use unrelated", `{"error":{"type":"invalid_request_error","message":"unexpected ` + "`tool_use_id`" + ` found in ` + "`tool_result`" + ` blocks"}}`, false},
		{"generic signature only", `{"error":{"type":"invalid_request_error","message":"invalid signature header"}}`, false},
		{"not json", `bad gateway`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isThinkingSignatureError([]byte(tc.body)); got != tc.want {
				t.Fatalf("isThinkingSignatureError(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestAnthConvertMessages_ToolResultIsErrorAlwaysEmitted pins that a tool_result
// block always serializes is_error, while other block types never carry
// it. wopr previously dropped is_error:false via omitempty on a shared struct.
func TestAnthConvertMessages_ToolResultIsErrorAlwaysEmitted(t *testing.T) {
	messages := []Message{ToolResultMessage{
		ToolCallID: "call_1", IsError: false,
		Content: []ToolResultMessageContent{TextContent{Text: "ok"}},
	}}
	out := anthConvertMessages(messages, false, false, false)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"is_error":false`) {
		t.Errorf("tool_result must emit is_error even when false: %s", raw)
	}
	// A plain text block must not carry is_error.
	textOut := anthConvertMessages([]Message{UserMessage{Content: UserContentBlocks{TextContent{Text: "hi"}}}}, false, false, false)
	rawText, _ := json.Marshal(textOut)
	if strings.Contains(string(rawText), "is_error") {
		t.Errorf("non-tool_result blocks must not carry is_error: %s", rawText)
	}
}

// TestApplyConversationCacheControl covers the cache breakpoint on the last user
// message: the last block of a final
// user message gets cache_control; a string-content final user message is lifted
// to a text block carrying it; a non-user final message is left untouched.
func TestApplyConversationCacheControl(t *testing.T) {
	cc := &anthCacheControl{Type: "ephemeral"}

	arrayMsgs := []anthMessage{{Role: "user", Content: []anthContentBlock{{Type: "text", Text: "thanks"}}}}
	applyConversationCacheControl(arrayMsgs, cc)
	if blocks := arrayMsgs[0].Content.([]anthContentBlock); blocks[len(blocks)-1].CacheControl != cc {
		t.Errorf("array last-user-block missing cache_control: %#v", blocks)
	}

	stringMsgs := []anthMessage{{Role: "user", Content: "thanks"}}
	applyConversationCacheControl(stringMsgs, cc)
	blocks, ok := stringMsgs[0].Content.([]anthContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "thanks" || blocks[0].CacheControl != cc {
		t.Errorf("string last-user-message not lifted to cached text block: %#v", stringMsgs[0].Content)
	}

	assistantLast := []anthMessage{{Role: "assistant", Content: []anthContentBlock{{Type: "text", Text: "done"}}}}
	applyConversationCacheControl(assistantLast, cc)
	if blocks := assistantLast[0].Content.([]anthContentBlock); blocks[0].CacheControl != nil {
		t.Errorf("non-user final message must not be cached: %#v", blocks)
	}
}

// ─── beta messages endpoint ─────────────────────────────────────────────────
